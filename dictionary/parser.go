package dictionary

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type File interface {
	io.Reader
	io.Closer
	Name() string
}

type Opener interface {
	OpenFile(name string) (File, error)
}

type FileSystemOpener struct {
}

func (f *FileSystemOpener) OpenFile(name string) (File, error) {
	absPath, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(absPath)
	return file, err
}

type Parser struct {
	Opener Opener
}

func (p *Parser) Parse(f File) (*Dictionary, error) {
	parsedFiles := map[string]struct{}{
		f.Name(): {},
	}
	dict := new(Dictionary)
	if err := p.parse(dict, parsedFiles, f); err != nil {
		return nil, err
	}
	return dict, nil
}

func (p *Parser) parse(dict *Dictionary, parsedFiles map[string]struct{}, f File) error {
	s := bufio.NewScanner(f)

	var vendorBlock *Vendor

	lineNo := 1
	for ; s.Scan(); lineNo++ {
		line := s.Text()
		if idx := strings.IndexByte(line, '#'); idx >= 0 {
			line = line[:idx]
		}
		if len(line) == 0 {
			continue
		}

		fields := strings.Fields(line)
		switch {
		case (len(fields) == 4 || len(fields) == 5) && fields[0] == "ATTRIBUTE":
			attr, err := p.parseAttribute(fields)
			if err != nil {
				return &ParseError{
					Inner: err,
					File:  f,
					Line:  lineNo,
				}
			}

			var existing *Attribute
			if vendorBlock == nil {
				existing = dict.AttributeByName(attr.Name)
			} else {
				existing = vendorBlock.AttributeByName(attr.Name)
			}
			if existing != nil {
				return &ParseError{
					Inner: &DuplicateAttributeError{
						Attribute: attr,
					},
					File: f,
					Line: lineNo,
				}
			}

			if vendorBlock == nil {
				dict.Attributes = append(dict.Attributes, attr)
			} else {
				vendorBlock.Attributes = append(vendorBlock.Attributes, attr)
			}

		case len(fields) == 4 && fields[0] == "VALUE":
			value, err := p.parseValue(fields)
			if err != nil {
				return &ParseError{
					Inner: err,
					File:  f,
					Line:  lineNo,
				}
			}

			// no duplicate check; VALUEs can be overwritten

			if vendorBlock == nil {
				dict.Values = append(dict.Values, value)
			} else {
				vendorBlock.Values = append(vendorBlock.Values, value)
			}

		case (len(fields) == 3 && len(fields) == 4) && fields[0] == "VENDOR":
			vendor, err := p.parseVendor(fields)
			if err != nil {
				return &ParseError{
					Inner: err,
					File:  f,
					Line:  lineNo,
				}
			}

			if existing := dict.vendorByNameOrNumber(vendor.Name, vendor.Number); existing != nil {
				return &ParseError{
					Inner: &DuplicateVendorError{
						Vendor: vendor,
					},
					File: f,
					Line: lineNo,
				}
			}

			dict.Vendors = append(dict.Vendors, vendor)

		case len(fields) == 2 && fields[0] == "BEGIN-VENDOR":
			// TODO: support RFC 6929 extended VSA?

			if vendorBlock != nil {
				return &ParseError{
					Inner: &NestedVendorBlockError{},
					File:  f,
					Line:  lineNo,
				}
			}

			vendor := dict.VendorByName(fields[1])
			if vendor == nil {
				return &ParseError{
					Inner: &UnknownVendorError{
						Vendor: fields[1],
					},
					File: f,
					Line: lineNo,
				}
			}

			vendorBlock = vendor

		case len(fields) == 2 && fields[0] == "END-VENDOR":
			if vendorBlock == nil {
				return &ParseError{
					Inner: &UnmatchedEndVendorError{},
					File:  f,
					Line:  lineNo,
				}
			}
			if vendorBlock.Name != fields[1] {
				return &ParseError{
					Inner: &InvalidEndVendorError{
						Vendor: fields[1],
					},
					File: f,
					Line: lineNo,
				}
			}

			vendorBlock = nil

		case len(fields) == 2 && fields[0] == "$INCLUDE":
			if vendorBlock != nil {
				return &ParseError{
					Inner: &BeginVendorIncludeError{},
					File:  f,
					Line:  lineNo,
				}
			}

			err := func() error {
				incFile, err := p.Opener.OpenFile(fields[1])
				if err != nil {
					return &ParseError{
						Inner: err,
						File:  f,
						Line:  lineNo,
					}
				}
				defer incFile.Close()

				incFileName := incFile.Name()
				if _, included := parsedFiles[incFileName]; included {
					return &ParseError{
						Inner: &RecursiveIncludeError{
							Filename: incFileName,
						},
						File: f,
						Line: lineNo,
					}
				}

				if err := p.parse(dict, parsedFiles, incFile); err != nil {
					return err
				}

				if err := incFile.Close(); err != nil {
					return &ParseError{
						Inner: err,
						File:  f,
						Line:  lineNo,
					}
				}

				return nil
			}()
			if err != nil {
				return err
			}

		default:
			return &ParseError{
				Inner: &UnknownLineError{
					Line: s.Text(),
				},
				File: f,
				Line: lineNo,
			}
		}
	}

	if err := s.Err(); err != nil {
		return err
	}

	if vendorBlock != nil {
		return &ParseError{
			Inner: &UnclosedVendorBlockError{},
			File:  f,
			Line:  lineNo - 1,
		}
	}

	return nil
}

func (p *Parser) ParseFile(filename string) (*Dictionary, error) {
	f, err := p.Opener.OpenFile(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return p.Parse(f)
}

func (p *Parser) parseAttribute(f []string) (*Attribute, error) {
	// 4 <= len(f) <= 5

	attr := &Attribute{
		Name: f[1],
		OID:  f[2],
	}

	switch {
	case f[3] == "string":
		attr.Type = AttributeString
	case f[3] == "octets":
		attr.Type = AttributeOctets
	case strings.HasPrefix(f[3], "octets[") && strings.HasSuffix(f[3], "]") && len(f[3]) > 8:
		size, err := strconv.ParseInt(f[3][7:len(f[3])-1], 10, 32)
		if err != nil {
			return nil, &UnknownAttributeTypeError{
				Type: f[3],
			}
		}
		attr.Size = new(int)
		*attr.Size = int(size)
		attr.Type = AttributeOctets
	case f[3] == "ipaddr":
		attr.Type = AttributeIPAddr
	case f[3] == "date":
		attr.Type = AttributeDate
	case f[3] == "integer":
		attr.Type = AttributeInteger
	case f[3] == "ipv6addr":
		attr.Type = AttributeIPv6Addr
	case f[3] == "ipv6prefix":
		attr.Type = AttributeIPv6Prefix
	case f[3] == "ifid":
		attr.Type = AttributeIFID
	case f[3] == "integer64":
		attr.Type = AttributeInteger64
	case f[3] == "vsa":
		attr.Type = AttributeVSA
	default:
		return nil, &UnknownAttributeTypeError{
			Type: f[3],
		}
	}

	if len(f) >= 5 {
		flags := strings.Split(f[4], ",")
		for _, f := range flags {
			switch {
			case strings.HasPrefix(f, "encrypt="):
				if attr.FlagEncrypt != nil {
					return nil, &DuplicateAttributeFlagError{
						Flag: f,
					}
				}
				encryptTypeStr := strings.TrimPrefix(f, "encrypt=")
				encryptType, err := strconv.ParseInt(encryptTypeStr, 10, 32)
				if err != nil {
					return nil, &InvalidAttributeEncryptTypeError{
						Type: encryptTypeStr,
					}
				}
				attr.FlagEncrypt = new(int)
				*attr.FlagEncrypt = int(encryptType)
			case f == "has_tag":
				if attr.FlagHasTag != nil {
					return nil, &DuplicateAttributeFlagError{
						Flag: f,
					}
				}
				attr.FlagHasTag = new(bool)
				*attr.FlagHasTag = true
			case f == "concat":
				if attr.FlagConcat != nil {
					return nil, &DuplicateAttributeFlagError{
						Flag: f,
					}
				}
				attr.FlagConcat = new(bool)
				*attr.FlagConcat = true
			default:
				return nil, &UnknownAttributeFlagError{
					Flag: f,
				}
			}
		}
	}

	return attr, nil
}

func (p *Parser) parseValue(f []string) (*Value, error) {
	// len(f) == 4

	value := &Value{
		Attribute: f[1],
		Name:      f[2],
	}

	number, err := strconv.ParseInt(f[3], 10, 32)
	if err != nil {
		return nil, err
	}
	value.Number = int(number)

	return value, nil
}

func (p *Parser) parseVendor(f []string) (*Vendor, error) {
	// 3 <= len(f) <= 4

	number, err := strconv.ParseInt(f[2], 10, 32)
	if err != nil {
		return nil, err
	}

	vendor := &Vendor{
		Name:         f[1],
		Number:       int(number),
		TypeOctets:   1,
		LengthOctets: 1,
	}

	if len(f) == 4 {
		// "format=t,l"
		// t ∈ [1, 2, 4]
		// l ∈ [0, 1, 2]
		if !strings.HasPrefix(f[3], "format=") || len(f[3]) != 10 || f[3][8] != ',' || (f[3][7] != '1' && f[3][7] != '2' && f[3][7] != '4') || (f[3][9] < '0' && f[3][9] > '2') {
			return nil, &InvalidVendorFormatError{
				Format: f[3],
			}
		}
		vendor.TypeOctets = int(f[3][7] - '0')
		vendor.LengthOctets = int(f[3][9] - '0')
	}

	return vendor, nil
}

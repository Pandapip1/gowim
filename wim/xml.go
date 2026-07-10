package wim

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// utf16BOM is the UTF-16LE byte order mark that prefixes a WIM XML document.
var utf16BOM = []byte{0xff, 0xfe}

// XMLData holds a WIM's XML metadata. On disk it is a UTF-16LE document,
// prefixed with a byte order mark, with a <WIM> root element containing one
// <IMAGE INDEX="n"> element per image plus WIM-level counters.
//
// This package preserves the decoded document text verbatim in Document and
// also parses out the per-image elements into Images for convenient access.
// Serialization re-encodes Document (not Images) to UTF-16LE with a BOM, so
// round-tripping is exact; to change the XML, edit Document.
//
// For Windows images, XMLImage additionally exposes the light-parsed
// <WINDOWS> sub-element (architecture, product name, edition ID,
// installation type, languages, version) via XMLImage.Windows, along with
// the sibling <DISPLAYNAME>/<DISPLAYDESCRIPTION>/<FLAGS> elements. These
// correspond to the fields DISM's `/Get-WimInfo` surfaces (Architecture,
// Edition, Installation, Language, Version); see XMLWindows and XMLVersion
// for details. As with all of Images, these are a read-only convenience view
// derived from Document at parse time — they do not feed back into
// AppendTo/EncodedLen.
type XMLData struct {
	// Document is the decoded (UTF-8) XML text, without the BOM.
	Document string
	// Images holds a light-parsed view of each <IMAGE> element.
	Images []XMLImage
}

// XMLImage is a light-parsed view of one <IMAGE> element in the WIM XML.
type XMLImage struct {
	// Index is the value of the INDEX attribute (1-based).
	Index int
	// Name is the text of the <NAME> child element, if present.
	Name string
	// Description is the text of the <DESCRIPTION> child element, if present.
	Description string
	// DirCount, FileCount, TotalBytes reflect the corresponding child
	// elements when present (0 if absent or unparseable).
	DirCount   uint64
	FileCount  uint64
	TotalBytes uint64

	// DisplayName is the text of the <DISPLAYNAME> child element, if
	// present. This is the human-friendly name DISM's /Get-WimInfo and
	// Windows Setup show for the edition (e.g. "Windows 11 Pro"), which can
	// differ from Name.
	DisplayName string
	// DisplayDescription is the text of the <DISPLAYDESCRIPTION> child
	// element, if present.
	DisplayDescription string
	// Flags is the text of the <FLAGS> child element, if present. In
	// practice this holds the same kind of edition-identifying token as
	// Windows.EditionID (e.g. "Professional").
	Flags string

	// Windows holds the light-parsed <WINDOWS> child element, or nil if the
	// <IMAGE> has no <WINDOWS> element (non-Windows or minimal images).
	// Confirmed present, with this shape, in a real Windows 11 23H2
	// install.esd's XML data resource (2026-07-10).
	Windows *XMLWindows
}

// XMLWindows is a light-parsed view of an <IMAGE>'s <WINDOWS> child element.
// Confirmed against a real Windows 11 23H2 install.esd's XML data resource
// (2026-07-10); see the excerpt in wim_test.go's TestXMLDataWindowsFields for
// the exact observed shape.
type XMLWindows struct {
	// Architecture is the raw numeric value of the <ARCH> element: a
	// PROCESSOR_ARCHITECTURE_* constant, the same encoding used by the
	// Win32 SYSTEM_INFO structure's wProcessorArchitecture member (0 = x86,
	// 5 = ARM, 6 = IA64, 9 = x64/AMD64, 12 = ARM64, 0xffff = unknown). The
	// raw value is kept (rather than only a decoded string) since it is
	// exactly what's on disk and new architectures may appear before this
	// package's decoder is updated; use ArchitectureName for a
	// human-readable label. See
	// https://learn.microsoft.com/en-us/windows/win32/api/sysinfoapi/ns-sysinfoapi-system_info
	Architecture int
	// ProductName is the text of <PRODUCTNAME>, if present (e.g. "Microsoft®
	// Windows® Operating System").
	ProductName string
	// EditionID is the text of <EDITIONID>, if present (e.g.
	// "Professional").
	EditionID string
	// InstallationType is the text of <INSTALLATIONTYPE>, if present (e.g.
	// "Client").
	InstallationType string
	// ProductType is the text of <PRODUCTTYPE>, if present (e.g. "WinNT").
	ProductType string
	// ProductSuite is the text of <PRODUCTSUITE>, if present.
	ProductSuite string
	// SystemRoot is the text of <SYSTEMROOT>, if present (e.g. "WINDOWS").
	SystemRoot string
	// Languages lists each <LANGUAGES><LANGUAGE> entry, if the <LANGUAGES>
	// element is present.
	Languages []string
	// DefaultLanguage is the text of <LANGUAGES><DEFAULT>, if present.
	DefaultLanguage string
	// Version holds the <VERSION> child element's fields, if present.
	Version *XMLVersion
}

// ArchitectureName returns a short human-readable label for w.Architecture
// (the PROCESSOR_ARCHITECTURE_* value), or "" if the value is not one of the
// documented constants.
func (w *XMLWindows) ArchitectureName() string {
	switch w.Architecture {
	case 0:
		return "x86"
	case 5:
		return "ARM"
	case 6:
		return "IA64"
	case 9:
		return "x64"
	case 12:
		return "ARM64"
	case 0xffff:
		return "unknown"
	default:
		return ""
	}
}

// XMLVersion is a light-parsed view of a <WINDOWS><VERSION> child element.
type XMLVersion struct {
	Major   int
	Minor   int
	Build   int
	SPBuild int
	SPLevel int
	// Branch is the text of <BRANCH>, if present (e.g.
	// "ni_release_svc_prod3").
	Branch string
}

// ParseXMLData decodes a WIM XML data resource. The input is the raw
// (uncompressed) resource bytes: a UTF-16LE document, normally starting with a
// BOM.
func ParseXMLData(raw []byte) (*XMLData, error) {
	body := raw
	if len(body) >= 2 && body[0] == 0xff && body[1] == 0xfe {
		body = body[2:] // strip BOM
	}
	doc := utf16leToString(body)
	// Trim a trailing NUL that some producers include.
	doc = strings.TrimRight(doc, "\x00")

	x := &XMLData{Document: doc}
	if err := x.parseImages(); err != nil {
		return nil, wrapErr("xml data", err)
	}
	return x, nil
}

// wimXMLRoot mirrors the <WIM> document structure for light parsing.
type wimXMLRoot struct {
	XMLName xml.Name      `xml:"WIM"`
	Images  []wimXMLImage `xml:"IMAGE"`
}

type wimXMLImage struct {
	Index              int            `xml:"INDEX,attr"`
	Name               string         `xml:"NAME"`
	Description        string         `xml:"DESCRIPTION"`
	DirCount           uint64         `xml:"DIRCOUNT"`
	FileCount          uint64         `xml:"FILECOUNT"`
	TotalBytes         uint64         `xml:"TOTALBYTES"`
	DisplayName        string         `xml:"DISPLAYNAME"`
	DisplayDescription string         `xml:"DISPLAYDESCRIPTION"`
	Flags              string         `xml:"FLAGS"`
	Windows            *wimXMLWindows `xml:"WINDOWS"`
}

// wimXMLWindows mirrors the <WINDOWS> element nested under <IMAGE> for light
// parsing. Field order/shape confirmed against a real Windows 11 23H2
// install.esd's XML data resource (2026-07-10).
type wimXMLWindows struct {
	Arch             int              `xml:"ARCH"`
	ProductName      string           `xml:"PRODUCTNAME"`
	EditionID        string           `xml:"EDITIONID"`
	InstallationType string           `xml:"INSTALLATIONTYPE"`
	ProductType      string           `xml:"PRODUCTTYPE"`
	ProductSuite     string           `xml:"PRODUCTSUITE"`
	SystemRoot       string           `xml:"SYSTEMROOT"`
	Languages        *wimXMLLanguages `xml:"LANGUAGES"`
	Version          *wimXMLVersion   `xml:"VERSION"`
}

type wimXMLLanguages struct {
	Language []string `xml:"LANGUAGE"`
	Default  string   `xml:"DEFAULT"`
}

type wimXMLVersion struct {
	Major   int    `xml:"MAJOR"`
	Minor   int    `xml:"MINOR"`
	Build   int    `xml:"BUILD"`
	SPBuild int    `xml:"SPBUILD"`
	SPLevel int    `xml:"SPLEVEL"`
	Branch  string `xml:"BRANCH"`
}

func (x *XMLData) parseImages() error {
	if strings.TrimSpace(x.Document) == "" {
		return nil
	}
	var root wimXMLRoot
	if err := xml.Unmarshal([]byte(x.Document), &root); err != nil {
		return fmt.Errorf("parse WIM XML: %w", err)
	}
	x.Images = x.Images[:0]
	for _, im := range root.Images {
		out := XMLImage{
			Index:              im.Index,
			Name:               im.Name,
			Description:        im.Description,
			DirCount:           im.DirCount,
			FileCount:          im.FileCount,
			TotalBytes:         im.TotalBytes,
			DisplayName:        im.DisplayName,
			DisplayDescription: im.DisplayDescription,
			Flags:              im.Flags,
		}
		if im.Windows != nil {
			w := &XMLWindows{
				Architecture:     im.Windows.Arch,
				ProductName:      im.Windows.ProductName,
				EditionID:        im.Windows.EditionID,
				InstallationType: im.Windows.InstallationType,
				ProductType:      im.Windows.ProductType,
				ProductSuite:     im.Windows.ProductSuite,
				SystemRoot:       im.Windows.SystemRoot,
			}
			if im.Windows.Languages != nil {
				w.Languages = im.Windows.Languages.Language
				w.DefaultLanguage = im.Windows.Languages.Default
			}
			if im.Windows.Version != nil {
				w.Version = &XMLVersion{
					Major:   im.Windows.Version.Major,
					Minor:   im.Windows.Version.Minor,
					Build:   im.Windows.Version.Build,
					SPBuild: im.Windows.Version.SPBuild,
					SPLevel: im.Windows.Version.SPLevel,
					Branch:  im.Windows.Version.Branch,
				}
			}
			out.Windows = w
		}
		x.Images = append(x.Images, out)
	}
	return nil
}

// AppendTo serializes the XML data, appending a UTF-16LE BOM followed by the
// UTF-16LE-encoded Document to dst.
func (x *XMLData) AppendTo(dst []byte) []byte {
	dst = append(dst, utf16BOM...)
	dst = append(dst, stringToUTF16LE(x.Document)...)
	return dst
}

// EncodedLen returns the number of bytes AppendTo will write.
func (x *XMLData) EncodedLen() int {
	return len(utf16BOM) + len(stringToUTF16LE(x.Document))
}

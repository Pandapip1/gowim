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
	Index       int    `xml:"INDEX,attr"`
	Name        string `xml:"NAME"`
	Description string `xml:"DESCRIPTION"`
	DirCount    uint64 `xml:"DIRCOUNT"`
	FileCount   uint64 `xml:"FILECOUNT"`
	TotalBytes  uint64 `xml:"TOTALBYTES"`
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
		x.Images = append(x.Images, XMLImage{
			Index:       im.Index,
			Name:        im.Name,
			Description: im.Description,
			DirCount:    im.DirCount,
			FileCount:   im.FileCount,
			TotalBytes:  im.TotalBytes,
		})
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

package appx

import (
	"encoding/xml"
	"fmt"
)

// ManifestNamespace is the XML namespace of an AppxManifest.xml's root
// <Package> element, confirmed against a real Windows 11 23H2
// AppxManifest.xml (2026-07-14, see appx.go's doc comment).
const ManifestNamespace = "http://schemas.microsoft.com/appx/manifest/foundation/windows10"

// Identity is an AppxManifest.xml's <Identity> element: the fields that
// together determine a package's full name, family name, and package
// family name (see the "Identity" element reference cited in appx.go).
type Identity struct {
	Name                  string `xml:"Name,attr"`
	Publisher             string `xml:"Publisher,attr"`
	Version               string `xml:"Version,attr"`
	ProcessorArchitecture string `xml:"ProcessorArchitecture,attr,omitempty"`
	ResourceId            string `xml:"ResourceId,attr,omitempty"`
}

// Manifest is a parsed AppxManifest.xml, modeling only its <Identity>
// child (see the package doc comment for why the rest is out of scope).
type Manifest struct {
	XMLName  xml.Name `xml:"Package"`
	Identity Identity `xml:"Identity"`
}

// ParseManifest decodes an AppxManifest.xml's <Identity> element from its
// raw bytes (plain UTF-8 XML). It does not validate the root namespace
// against ManifestNamespace: encoding/xml's default element matching
// already requires the child <Identity> element (unprefixed, inheriting
// the default xmlns) to be present, and this package has no need to reject
// a differently-namespaced root the way mum.Parse does (a real
// AppxManifest.xml has never been observed with anything else).
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := xml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("appx: parse manifest: %w", err)
	}
	return &m, nil
}

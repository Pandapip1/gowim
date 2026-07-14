package appx

import (
	"encoding/xml"
	"fmt"
)

// ProvisioningNamespace is the XML namespace of AppxProvisioning.xml's root
// <AppxProvisionList> element, confirmed against a real Windows 11 23H2
// ProgramData\Microsoft\Windows\AppxProvisioning.xml (2026-07-14, see
// appx.go's doc comment). Not documented by Microsoft under this filename;
// reverse-engineered by direct inspection of the real file.
const ProvisioningNamespace = "http://schemas.microsoft.com/appx/2013/appxprovisionpackage"

// ProvisionedPackage is one <Package> entry under <Provisioned>: a package,
// bundle, framework, or resource package provisioned for all users.
type ProvisionedPackage struct {
	FullName                string `xml:"FullName,attr"`
	PackageType             string `xml:"PackageType,attr,omitempty"`
	ProvisionSourceIsBundle bool   `xml:"ProvisionSourceIsBundle,attr,omitempty"`
	IsLOBApp                bool   `xml:"IsLOBApp,attr,omitempty"`
}

// EndOfLifePackage is one <Package> entry under <EndOfLife>: a package
// family blocked from (re)provisioning.
type EndOfLifePackage struct {
	FamilyName string `xml:"FamilyName,attr"`
}

// ProvisionList is a parsed AppxProvisioning.xml: the offline (pre-boot)
// source of truth for which AppX packages a Windows image provisions for
// all users, and which package families are blocked from being
// (re)provisioned. See appx.go's doc comment for provenance.
type ProvisionList struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/appx/2013/appxprovisionpackage AppxProvisionList"`

	EndOfLife   []EndOfLifePackage   `xml:"EndOfLife>Package"`
	Provisioned []ProvisionedPackage `xml:"Provisioned>Package"`
}

// ParseProvisioning decodes an AppxProvisioning.xml from its raw bytes
// (plain UTF-8 XML, no PA30 or other compression involved).
func ParseProvisioning(data []byte) (*ProvisionList, error) {
	var pl ProvisionList
	if err := xml.Unmarshal(data, &pl); err != nil {
		return nil, fmt.Errorf("appx: parse provisioning list: %w", err)
	}
	return &pl, nil
}

// Serialize encodes pl back to an AppxProvisioning.xml-shaped XML document.
// Real Windows-produced files are a single line with no indentation and no
// standalone attribute in the XML declaration (see the real fixture in
// testdata) - Serialize matches that shape byte-for-byte only for a
// ProvisionList that round-trips through Parse unchanged, since (like the
// sibling mum package) it does not preserve unknown attributes/elements.
func (pl *ProvisionList) Serialize() ([]byte, error) {
	body, err := xml.Marshal(pl)
	if err != nil {
		return nil, fmt.Errorf("appx: serialize provisioning list: %w", err)
	}
	out := make([]byte, 0, len(xmlProlog)+len(body))
	out = append(out, xmlProlog...)
	out = append(out, body...)
	return out, nil
}

// xmlProlog is the XML declaration Serialize emits, matching the real
// fixture's prolog exactly (no standalone attribute, unlike mum's).
const xmlProlog = `<?xml version="1.0" encoding="utf-8"?>`

// Package mum implements parsing and serialization of the "asm.v3" CBS
// (Component-Based Servicing) manifest XML shape used by two related but
// distinct file classes: package-level servicing manifests (.mum files,
// e.g. Windows\servicing\Packages\*.mum) and component-level WinSxS
// manifests (Windows\WinSxS\Manifests\*.manifest, once decompressed from
// PA30 by the sibling `pa30` package -- this package itself only handles
// the XML, not PA30 decompression).
//
// Scope: the base <assembly>/<assemblyIdentity> schema is documented by
// Microsoft (see
// https://learn.microsoft.com/en-us/windows/win32/sbscs/assembly-manifests
// and https://learn.microsoft.com/en-us/windows/win32/sbscs/manifest-file-schema).
// The CBS-specific asm.v3 vocabulary modeled here is NOT documented
// anywhere found; it was empirically inferred by sampling real files:
//
//   - Package-level (.mum) vocabulary -- <package>, <update>, <parent>,
//     <installerAssembly>, <selectable>, <detectNone>, <declareCapability>,
//     <component> (as an <update> child) -- from 1262 real .mum files
//     (2026-07-10) plus a further real-VM cross-check (2026-07-13),
//     recorded in this repo's top-level TODO.md.
//   - Component-level (.manifest) vocabulary -- <deployment/>,
//     <dependency>, <dependentAssembly> -- from 19 real .manifest files
//     successfully decoded by the sibling `pa30` package (2026-07-13),
//     also recorded in TODO.md. A given real manifest has been observed to
//     use one vocabulary or the other, never both.
//
// Elements outside this modeled set (e.g. vendor extensions like
// <mum2:customInformation>, <driver>, <satelliteInfo>, <MutualExclusionGroup>)
// are silently ignored on Parse and dropped on Serialize -- this package is
// not a lossless round-trip of arbitrary manifest content, only of the
// subset needed to identify a package/component's identity, its declared
// payload/component references, and its dependency edges.
package mum

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// wrapErr is a small helper for adding context to parse/serialize errors
// without pulling in a dependency.
func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("mum: %s: %w", what, err)
}

// Namespace is the XML namespace of a servicing package manifest's root
// <assembly> element, confirmed against real Windows 11 23H2 .mum files.
// Serialize always emits this namespace, regardless of which recognized
// namespace (see recognizedNamespaces) the source document used.
const Namespace = "urn:schemas-microsoft-com:asm.v3"

// recognizedNamespaces are the root <assembly> namespaces Parse accepts.
// asm.v1/v2 are Microsoft's own older, documented base SxS assembly-manifest
// namespaces (see package docs) -- confirmed necessary 2026-07-13: some real
// `Windows\WinSxS\Manifests\*.manifest` files (older, pre-CBS runtime
// component manifests, e.g. the VC++ 8.0/9.0 CRT) are plain, uncompressed
// asm.v1 XML rather than PA30-compressed asm.v3, found while measuring this
// project's `pa30`/`component` packages' decode coverage against a full
// real image's Manifests directory (17189 files).
var recognizedNamespaces = map[string]bool{
	"urn:schemas-microsoft-com:asm.v1": true,
	"urn:schemas-microsoft-com:asm.v2": true,
	Namespace:                          true,
}

// xmlProlog is the XML declaration Serialize emits. Real Windows-produced
// .mum files are inconsistent here: some include standalone="yes", others
// omit it (both observed in real Windows 11 23H2 samples), so this is not a
// byte-exact match for every real file, just a valid, representative prolog.
const xmlProlog = `<?xml version="1.0" encoding="utf-8" standalone="yes"?>` + "\n"

// yesNoBool is a bool that also accepts "yes"/"no" (in addition to the
// standard "true"/"false") when unmarshaling an XML attribute, always
// serializing back out as "true"/"false". Confirmed necessary 2026-07-13 by
// decoding every real `.manifest` file in a real Windows 11 23H2 image's
// `Windows\WinSxS\Manifests` (17189 files, via the sibling `pa30` package):
// `discoverable` attributes use "no"/"yes" in the overwhelming majority of
// real samples (7043 "no" + 4400 "yes" vs. only 368 "false", and no "true"
// observed at all) -- encoding/xml's default bool handling only accepts
// "true"/"false" and errors on anything else, which is what this type
// works around.
type yesNoBool bool

func (b *yesNoBool) UnmarshalXMLAttr(attr xml.Attr) error {
	switch strings.ToLower(attr.Value) {
	case "true", "yes":
		*b = true
	case "false", "no":
		*b = false
	default:
		return fmt.Errorf("invalid boolean attribute %s=%q", attr.Name.Local, attr.Value)
	}
	return nil
}

func (b yesNoBool) MarshalXMLAttr(name xml.Name) (xml.Attr, error) {
	v := "false"
	if b {
		v = "true"
	}
	return xml.Attr{Name: name, Value: v}, nil
}

// AssemblyIdentity identifies a package/component/deployment by SxS
// assembly identity -- the same identity shape used throughout WinSxS
// (matching the documented assemblyIdentity element, see package docs).
type AssemblyIdentity struct {
	Name                  string `xml:"name,attr"`
	Version               string `xml:"version,attr"`
	ProcessorArchitecture string `xml:"processorArchitecture,attr,omitempty"`
	Language              string `xml:"language,attr,omitempty"`
	BuildType             string `xml:"buildType,attr,omitempty"`
	PublicKeyToken        string `xml:"publicKeyToken,attr,omitempty"`
	// VersionScope holds values like "nonSxS", seen on <installerAssembly>
	// and some nested <component> identities.
	VersionScope string `xml:"versionScope,attr,omitempty"`
}

// CapabilityIdentity identifies a capability token declared/required by
// <declareCapability> (distinct from AssemblyIdentity: no
// processorArchitecture/publicKeyToken observed in real samples).
type CapabilityIdentity struct {
	Name     string `xml:"name,attr"`
	Version  string `xml:"version,attr,omitempty"`
	Language string `xml:"language,attr,omitempty"`
}

// DetectNone is a <detectNone> element: whether a <selectable> update is
// considered present when no other detection signal fires.
type DetectNone struct {
	Default yesNoBool `xml:"default,attr"`
}

// Selectable marks an <update> as an independently enable/disable-able
// optional feature.
type Selectable struct {
	Disposition string      `xml:"disposition,attr,omitempty"`
	DetectNone  *DetectNone `xml:"detectNone"`
}

// Parent is a manifest's <parent> element: the package this one attaches to
// (e.g. a KB wrapper's parent edition package).
type Parent struct {
	BuildCompare    string `xml:"buildCompare,attr,omitempty"`
	RevisionCompare string `xml:"revisionCompare,attr,omitempty"`
	Integrate       string `xml:"integrate,attr,omitempty"`
	Disposition     string `xml:"disposition,attr,omitempty"`

	Identity AssemblyIdentity `xml:"assemblyIdentity"`
}

// Component is a <component> element nested inside an <update>, an
// alternative to NestedPackage seen in language-feature manifests: it
// references a component-level (not package-level) identity.
type Component struct {
	Identity AssemblyIdentity `xml:"assemblyIdentity"`
}

// NestedPackage is a <package> element nested inside an <update>: a
// reference to another package this update pulls in.
type NestedPackage struct {
	Contained yesNoBool `xml:"contained,attr,omitempty"`
	Integrate string    `xml:"integrate,attr,omitempty"`

	Identity AssemblyIdentity `xml:"assemblyIdentity"`
}

// Update is one <update> entry under a manifest's <package>: a named
// sub-unit of the package, optionally selectable, that references a nested
// package or component identity.
type Update struct {
	Name        string `xml:"name,attr"`
	DisplayName string `xml:"displayName,attr,omitempty"`
	Description string `xml:"description,attr,omitempty"`

	Selectable *Selectable    `xml:"selectable"`
	Package    *NestedPackage `xml:"package"`
	Component  *Component     `xml:"component"`
}

// DeclareCapability records a <declareCapability> element: the capability
// token a package provides, and the capability tokens it depends on. Seen
// mainly in language-feature ("OnDemand Pack") manifests.
type DeclareCapability struct {
	Capability   *CapabilityIdentity  `xml:"capability>capabilityIdentity"`
	Dependencies []CapabilityIdentity `xml:"dependency>capabilityIdentity"`
}

// Deployment is a <deployment/> marker element: an always-empty element
// seen in every component-level (WinSxS `.manifest`) manifest sampled so
// far (19 real files, decoded via the sibling `pa30` package, 2026-07-13).
// Its exact meaning is unconfirmed -- it carries no attributes in any
// sample seen -- but its mere presence distinguishes a component-level
// manifest from a package-level `.mum` file, which uses <package>/<update>
// instead.
type Deployment struct{}

// DependentAssembly is a <dependentAssembly> element nested in a
// <dependency>: the identity of another assembly/component this manifest
// requires.
type DependentAssembly struct {
	DependencyType string           `xml:"dependencyType,attr,omitempty"`
	Identity       AssemblyIdentity `xml:"assemblyIdentity"`
}

// Dependency is a <dependency> element: one or more required assemblies,
// seen in component-level (WinSxS `.manifest`) manifests. This is a
// different vocabulary from `.mum` package-level manifests' <update>/
// <package> nesting, even though both express "this depends on that".
type Dependency struct {
	Discoverable      yesNoBool           `xml:"discoverable,attr"`
	DependentAssembly []DependentAssembly `xml:"dependentAssembly"`
}

// Package is a manifest's <package> element: the servicing package this
// manifest declares, and its relationships to other packages/components.
type Package struct {
	Identifier  string `xml:"identifier,attr,omitempty"`
	ReleaseType string `xml:"releaseType,attr,omitempty"`
	Restart     string `xml:"restart,attr,omitempty"`

	Parent            *Parent            `xml:"parent"`
	InstallerAssembly *AssemblyIdentity  `xml:"installerAssembly"`
	DeclareCapability *DeclareCapability `xml:"declareCapability"`
	Updates           []Update           `xml:"update"`
}

// Manifest is a parsed servicing package manifest (.mum file): a plain-XML
// document rooted at <assembly xmlns="urn:schemas-microsoft-com:asm.v3">
// (Parse also accepts the older asm.v1/v2 namespaces -- see
// recognizedNamespaces -- but Serialize always writes this one back out).
type Manifest struct {
	XMLName xml.Name `xml:"urn:schemas-microsoft-com:asm.v3 assembly"`

	ManifestVersion     string `xml:"manifestVersion,attr,omitempty"`
	Copyright           string `xml:"copyright,attr,omitempty"`
	Description         string `xml:"description,attr,omitempty"`
	DisplayName         string `xml:"displayName,attr,omitempty"`
	Company             string `xml:"company,attr,omitempty"`
	SupportInformation  string `xml:"supportInformation,attr,omitempty"`
	CreationTimeStamp   string `xml:"creationTimeStamp,attr,omitempty"`
	LastUpdateTimeStamp string `xml:"lastUpdateTimeStamp,attr,omitempty"`

	Identity AssemblyIdentity `xml:"assemblyIdentity"`
	Package  *Package         `xml:"package"`

	// Deployment and Dependencies are the component-level (WinSxS
	// `.manifest`) vocabulary, an alternative to Package/Updates -- see
	// their type docs. Real samples seen so far have one or the other, not
	// both.
	Deployment   *Deployment  `xml:"deployment"`
	Dependencies []Dependency `xml:"dependency"`
}

// manifestAlias mirrors Manifest field-for-field (Go allows converting
// between struct types with identical field names/order/types, ignoring
// tags, since Go 1.8), except its XMLName tag carries no namespace --
// unlike Manifest's own tag, which is pinned to asm.v3 so Serialize always
// emits that namespace regardless of what was parsed. UnmarshalXML uses this
// to decode the body of any recognized-namespace root element without
// Manifest's own fixed-namespace tag rejecting asm.v1/v2 input.
type manifestAlias struct {
	XMLName xml.Name `xml:"assembly"`

	ManifestVersion     string `xml:"manifestVersion,attr,omitempty"`
	Copyright           string `xml:"copyright,attr,omitempty"`
	Description         string `xml:"description,attr,omitempty"`
	DisplayName         string `xml:"displayName,attr,omitempty"`
	Company             string `xml:"company,attr,omitempty"`
	SupportInformation  string `xml:"supportInformation,attr,omitempty"`
	CreationTimeStamp   string `xml:"creationTimeStamp,attr,omitempty"`
	LastUpdateTimeStamp string `xml:"lastUpdateTimeStamp,attr,omitempty"`

	Identity AssemblyIdentity `xml:"assemblyIdentity"`
	Package  *Package         `xml:"package"`

	Deployment   *Deployment  `xml:"deployment"`
	Dependencies []Dependency `xml:"dependency"`
}

// UnmarshalXML accepts a root <assembly> element in any of
// recognizedNamespaces (asm.v1/v2/v3), rejecting anything else outright as
// not a recognized SxS assembly manifest at all. Real `.manifest` files use
// more than one of these in practice -- see recognizedNamespaces' doc
// comment -- which plain reflection-based unmarshaling can't express, since
// a struct tag's namespace is a single fixed value.
func (m *Manifest) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if !recognizedNamespaces[start.Name.Space] {
		return fmt.Errorf("mum: unrecognized root element namespace %q", start.Name.Space)
	}
	var alias manifestAlias
	if err := d.DecodeElement(&alias, &start); err != nil {
		return err
	}
	*m = Manifest(alias)
	return nil
}

// Parse decodes a servicing package manifest from its raw .mum file bytes
// (plain UTF-8 XML). Elements/attributes not modeled by this package (see
// package docs) are silently ignored, matching encoding/xml's default
// unknown-element behavior. See Manifest.UnmarshalXML for the root
// namespace check.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := xml.Unmarshal(data, &m); err != nil {
		return nil, wrapErr("parse manifest", err)
	}
	return &m, nil
}

// Serialize encodes m back to a .mum-shaped XML document: an XML
// declaration matching real Windows-produced manifests' prolog, followed by
// the two-space-indented <assembly> tree. Only the fields modeled by this
// package's types are emitted; see the package doc comment for what is not
// round-tripped. The root element's namespace is always Namespace (asm.v3),
// even if m was Parsed from an older asm.v1/v2 document -- this package
// does not preserve which recognized namespace a manifest originally used
// (Manifest's XMLName tag is pinned to asm.v3; see manifestAlias's doc
// comment).
func (m *Manifest) Serialize() ([]byte, error) {
	body, err := xml.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, wrapErr("serialize manifest", err)
	}
	out := make([]byte, 0, len(xmlProlog)+len(body))
	out = append(out, xmlProlog...)
	out = append(out, body...)
	return out, nil
}

// Package mum implements parsing and serialization of Windows servicing
// package manifests (.mum files, e.g. Windows\servicing\Packages\*.mum): the
// plain-XML "asm.v3" documents CBS (Component-Based Servicing) uses to
// declare a package's identity, its relationship to other packages
// (parent/update/dependency), and optional-feature selectability.
//
// Scope: the base <assembly>/<assemblyIdentity> schema is documented by
// Microsoft (see
// https://learn.microsoft.com/en-us/windows/win32/sbscs/assembly-manifests
// and https://learn.microsoft.com/en-us/windows/win32/sbscs/manifest-file-schema).
// The CBS-specific asm.v3 vocabulary modeled here (<package>, <update>,
// <parent>, <installerAssembly>, <selectable>, <detectNone>,
// <declareCapability>, <component>) is NOT documented anywhere found; it was
// empirically inferred by sampling real .mum files (1262 files from a real
// Windows 11 23H2 image on 2026-07-10, plus a further real-VM cross-check on
// 2026-07-13 whose findings are recorded in this repo's top-level TODO.md).
// Elements outside this modeled set (e.g. vendor extensions like
// <mum2:customInformation>, <driver>, <satelliteInfo>, <MutualExclusionGroup>)
// are silently ignored on Parse and dropped on Serialize -- this package is
// not a lossless round-trip of arbitrary .mum content, only of the subset
// needed to identify a package's identity, its declared payload/component
// references, and its dependency edges.
//
// This package does NOT read WinSxS `.manifest` files (as opposed to
// `.mum` files): those are PA30-delta-compressed, a separate, still
// under-research format documented in TODO.md, not plain XML.
package mum

import (
	"encoding/xml"
	"fmt"
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
const Namespace = "urn:schemas-microsoft-com:asm.v3"

// xmlProlog is the XML declaration Serialize emits. Real Windows-produced
// .mum files are inconsistent here: some include standalone="yes", others
// omit it (both observed in real Windows 11 23H2 samples), so this is not a
// byte-exact match for every real file, just a valid, representative prolog.
const xmlProlog = `<?xml version="1.0" encoding="utf-8" standalone="yes"?>` + "\n"

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
	Default bool `xml:"default,attr"`
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
	Contained bool   `xml:"contained,attr,omitempty"`
	Integrate string `xml:"integrate,attr,omitempty"`

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
// document rooted at <assembly xmlns="urn:schemas-microsoft-com:asm.v3">.
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
}

// Parse decodes a servicing package manifest from its raw .mum file bytes
// (plain UTF-8 XML). Elements/attributes not modeled by this package (see
// package docs) are silently ignored, matching encoding/xml's default
// unknown-element behavior.
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
// round-tripped.
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

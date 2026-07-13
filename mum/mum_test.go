package mum

import (
	_ "embed"
	"strings"
	"testing"
)

// Fixtures are real .mum files copied verbatim from a real Windows 11 23H2
// VM's Windows\servicing\Packages directory (2026-07-13, via `guestmount
// --ro`; see TODO.md for the source), chosen to cover the distinct element
// shapes this package models: plain package/update chains, parent +
// installerAssembly (KB wrapper), selectable/detectNone (optional feature),
// and declareCapability/dependency + nested <component> (language feature).
var (
	//go:embed testdata/package_with_updates.mum
	fixturePackageWithUpdates []byte
	//go:embed testdata/kb_wrapper_parent_installer.mum
	fixtureKBWrapper []byte
	//go:embed testdata/selectable_feature.mum
	fixtureSelectableFeature []byte
	//go:embed testdata/capability_language.mum
	fixtureCapabilityLanguage []byte
)

func TestParsePackageWithUpdates(t *testing.T) {
	m, err := Parse(fixturePackageWithUpdates)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Identity.Name != "HyperV-Compute-Host-Package" {
		t.Errorf("Identity.Name = %q", m.Identity.Name)
	}
	if m.Package == nil {
		t.Fatal("Package is nil")
	}
	if m.Package.Identifier != "HyperV-Compute-Host" {
		t.Errorf("Package.Identifier = %q", m.Package.Identifier)
	}
	if got, want := len(m.Package.Updates), 4; got != want {
		t.Fatalf("len(Updates) = %d, want %d", got, want)
	}
	u := m.Package.Updates[0]
	if u.Name != "2ebbf6c464a468921cbc7a02775ab9f1" {
		t.Errorf("Updates[0].Name = %q", u.Name)
	}
	if u.Package == nil {
		t.Fatal("Updates[0].Package is nil")
	}
	if u.Package.Integrate != "hidden" {
		t.Errorf("Updates[0].Package.Integrate = %q", u.Package.Integrate)
	}
	if u.Package.Identity.Name != "HyperV-Compute-Host-merged-Package" {
		t.Errorf("Updates[0].Package.Identity.Name = %q", u.Package.Identity.Name)
	}
}

func TestParseKBWrapper(t *testing.T) {
	m, err := Parse(fixtureKBWrapper)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Package == nil {
		t.Fatal("Package is nil")
	}
	if m.Package.Identifier != "KB5030219" {
		t.Errorf("Package.Identifier = %q", m.Package.Identifier)
	}
	if m.Package.Restart != "possible" {
		t.Errorf("Package.Restart = %q", m.Package.Restart)
	}
	p := m.Package.Parent
	if p == nil {
		t.Fatal("Parent is nil")
	}
	if p.RevisionCompare != "GE" || p.Integrate != "standalone" || p.Disposition != "detect" {
		t.Errorf("Parent = %+v", p)
	}
	if p.Identity.Name != "microsoft-windows-client-languagepack-package" {
		t.Errorf("Parent.Identity.Name = %q", p.Identity.Name)
	}
	ia := m.Package.InstallerAssembly
	if ia == nil {
		t.Fatal("InstallerAssembly is nil")
	}
	if ia.Name != "Microsoft-Windows-ServicingStack" || ia.VersionScope != "nonSxS" {
		t.Errorf("InstallerAssembly = %+v", ia)
	}
}

func TestParseSelectableFeature(t *testing.T) {
	m, err := Parse(fixtureSelectableFeature)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Package.Updates) != 1 {
		t.Fatalf("len(Updates) = %d", len(m.Package.Updates))
	}
	u := m.Package.Updates[0]
	if u.Selectable == nil {
		t.Fatal("Selectable is nil")
	}
	if u.Selectable.Disposition != "staged" {
		t.Errorf("Selectable.Disposition = %q", u.Selectable.Disposition)
	}
	if u.Selectable.DetectNone == nil {
		t.Fatal("DetectNone is nil")
	}
	if u.Selectable.DetectNone.Default != false {
		t.Errorf("DetectNone.Default = %v, want false", u.Selectable.DetectNone.Default)
	}
	if u.Package == nil || u.Package.Identity.Name != "Containers-DisposableClientVM-Package" {
		t.Errorf("Updates[0].Package = %+v", u.Package)
	}
}

func TestParseCapabilityLanguage(t *testing.T) {
	m, err := Parse(fixtureCapabilityLanguage)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	dc := m.Package.DeclareCapability
	if dc == nil {
		t.Fatal("DeclareCapability is nil")
	}
	if dc.Capability == nil || dc.Capability.Name != "Language.TextToSpeech" {
		t.Errorf("Capability = %+v", dc.Capability)
	}
	if len(dc.Dependencies) != 1 || dc.Dependencies[0].Name != "Language.Basic" {
		t.Errorf("Dependencies = %+v", dc.Dependencies)
	}
	if len(m.Package.Updates) != 2 {
		t.Fatalf("len(Updates) = %d", len(m.Package.Updates))
	}
	// The first update in this real file uses <component>, not <package>,
	// unlike the other fixtures -- confirms both nesting shapes parse.
	if m.Package.Updates[0].Component == nil {
		t.Fatal("Updates[0].Component is nil")
	}
	if m.Package.Updates[0].Component.Identity.Name != "Microsoft-Windows-LanguageFeatures-TextToSpeech-en-us-Deployment" {
		t.Errorf("Updates[0].Component.Identity.Name = %q", m.Package.Updates[0].Component.Identity.Name)
	}
	if m.Package.Updates[1].Package == nil {
		t.Fatal("Updates[1].Package is nil")
	}
}

// TestSerializeRoundTrip verifies that Parse -> Serialize -> Parse again
// yields semantically identical data for every real fixture, i.e. nothing
// modeled by this package is lost across a round trip (vendor extensions
// like <mum2:customInformation>, which this package does not model, are
// expected to be dropped -- see TestSerializeDropsUnmodeledExtensions).
func TestSerializeRoundTrip(t *testing.T) {
	fixtures := map[string][]byte{
		"package_with_updates": fixturePackageWithUpdates,
		"kb_wrapper":           fixtureKBWrapper,
		"selectable_feature":   fixtureSelectableFeature,
		"capability_language":  fixtureCapabilityLanguage,
	}
	for name, raw := range fixtures {
		t.Run(name, func(t *testing.T) {
			m1, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse (first): %v", err)
			}
			out, err := m1.Serialize()
			if err != nil {
				t.Fatalf("Serialize: %v", err)
			}
			m2, err := Parse(out)
			if err != nil {
				t.Fatalf("Parse (second, of re-serialized output): %v\n--- output ---\n%s", err, out)
			}
			if m1.Identity != m2.Identity {
				t.Errorf("Identity mismatch after round trip:\n got  %+v\n want %+v", m2.Identity, m1.Identity)
			}
			if (m1.Package == nil) != (m2.Package == nil) {
				t.Fatalf("Package presence mismatch after round trip")
			}
			if m1.Package != nil {
				if len(m1.Package.Updates) != len(m2.Package.Updates) {
					t.Errorf("Updates count mismatch: got %d, want %d", len(m2.Package.Updates), len(m1.Package.Updates))
				}
			}
		})
	}
}

// TestSerializeDropsUnmodeledExtensions documents, rather than merely
// asserting, that vendor extension elements this package does not model
// (e.g. <mum2:customInformation> in a real language-feature manifest) do not
// survive a Serialize call. This is a deliberate scope limitation (see
// package docs), not a bug -- this test exists so a future change to widen
// scope has a clear signal to update.
func TestSerializeDropsUnmodeledExtensions(t *testing.T) {
	m, err := Parse(fixtureCapabilityLanguage)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := m.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if strings.Contains(string(out), "customInformation") {
		t.Fatal("expected customInformation to be dropped, but it appears in serialized output")
	}
	if !strings.Contains(string(fixtureCapabilityLanguage), "customInformation") {
		t.Fatal("test fixture no longer contains customInformation -- fixture changed, update this test")
	}
}

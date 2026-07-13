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

	// fixtureComponentManifest is a real component-level manifest -- the
	// decompressed content of a real WinSxS `.manifest` file (identity
	// 022bd29263008e5688235b714058746f, 4.0.15912.251, amd64), produced by
	// the sibling `pa30` package's DecodeWithSource (2026-07-13) and
	// cross-validated there against an independent SHA-256 (see
	// pa30/pa30_test.go and TODO.md's "S256H mystery resolved" entry). This
	// is the <deployment>/<dependency> vocabulary, not the <package>/
	// <update> vocabulary the other fixtures use.
	//go:embed testdata/component_manifest_sample.xml
	fixtureComponentManifest []byte
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

func TestParseComponentManifest(t *testing.T) {
	m, err := Parse(fixtureComponentManifest)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Identity.Name != "022bd29263008e5688235b714058746f" {
		t.Errorf("Identity.Name = %q", m.Identity.Name)
	}
	if m.Identity.VersionScope != "nonSxS" {
		t.Errorf("Identity.VersionScope = %q", m.Identity.VersionScope)
	}
	if m.Deployment == nil {
		t.Fatal("Deployment is nil")
	}
	if m.Package != nil {
		t.Errorf("Package = %+v, want nil (component manifests use Dependencies, not Package)", m.Package)
	}
	if len(m.Dependencies) != 1 {
		t.Fatalf("len(Dependencies) = %d, want 1", len(m.Dependencies))
	}
	dep := m.Dependencies[0]
	if dep.Discoverable {
		t.Errorf("Dependencies[0].Discoverable = true, want false")
	}
	if len(dep.DependentAssembly) != 1 {
		t.Fatalf("len(DependentAssembly) = %d, want 1", len(dep.DependentAssembly))
	}
	da := dep.DependentAssembly[0]
	if da.DependencyType != "install" {
		t.Errorf("DependentAssembly[0].DependencyType = %q", da.DependencyType)
	}
	if da.Identity.Name != "System" {
		t.Errorf("DependentAssembly[0].Identity.Name = %q", da.Identity.Name)
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
		"component_manifest":   fixtureComponentManifest,
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
			if (m1.Deployment == nil) != (m2.Deployment == nil) {
				t.Errorf("Deployment presence mismatch after round trip")
			}
			if len(m1.Dependencies) != len(m2.Dependencies) {
				t.Errorf("Dependencies count mismatch: got %d, want %d", len(m2.Dependencies), len(m1.Dependencies))
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

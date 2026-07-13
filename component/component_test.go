package component

import (
	_ "embed"
	"errors"
	"testing"
)

var errTest = errors.New("synthetic test error")

var (
	// Real .mum files, copied from the sibling mum package's own testdata
	// (see mum/README.md for their provenance: a real Windows 11 23H2 VM,
	// 2026-07-13).
	//go:embed testdata/package_with_updates.mum
	fixturePackageWithUpdates []byte
	//go:embed testdata/kb_wrapper_parent_installer.mum
	fixtureKBWrapper []byte

	// A real WinSxS `.manifest` file (raw, DCM+PA30-prefixed on-disk form)
	// and the real shared dictionary needed to decode it -- both copied
	// from the sibling pa30 package's testdata (see pa30/README.md).
	//go:embed testdata/component_sample.manifest
	fixtureComponentManifest []byte
	//go:embed testdata/wcp_dictionary.bin
	fixtureDictionary []byte
)

func TestParseMUMPackageWithUpdates(t *testing.T) {
	e := ParseMUM("HyperV-Compute-Host-Package.mum", fixturePackageWithUpdates)
	if e.Err != nil {
		t.Fatalf("ParseMUM: %v", e.Err)
	}
	if e.Kind != KindPackage {
		t.Errorf("Kind = %v, want KindPackage", e.Kind)
	}
	if e.Identity.Name != "HyperV-Compute-Host-Package" {
		t.Errorf("Identity.Name = %q", e.Identity.Name)
	}
	// package_with_updates.mum has 4 <update><package> children, each a
	// dependency, plus no parent/installerAssembly.
	if len(e.Dependencies) != 4 {
		t.Fatalf("len(Dependencies) = %d, want 4", len(e.Dependencies))
	}
	if e.Dependencies[0].Name != "HyperV-Compute-Host-merged-Package" {
		t.Errorf("Dependencies[0].Name = %q", e.Dependencies[0].Name)
	}
}

func TestParseMUMKBWrapper(t *testing.T) {
	e := ParseMUM("KB5030219.mum", fixtureKBWrapper)
	if e.Err != nil {
		t.Fatalf("ParseMUM: %v", e.Err)
	}
	// Parent + installerAssembly + 1 <update><package> = 3 dependencies.
	if len(e.Dependencies) != 3 {
		t.Fatalf("len(Dependencies) = %d, want 3: %+v", len(e.Dependencies), e.Dependencies)
	}
	names := map[string]bool{}
	for _, d := range e.Dependencies {
		names[d.Name] = true
	}
	for _, want := range []string{
		"microsoft-windows-client-languagepack-package", // parent
		"Microsoft-Windows-ServicingStack",              // installerAssembly
		"microsoft-windows-client-languagepack-package", // update->package (same name, different version -- both recorded)
	} {
		if !names[want] {
			t.Errorf("Dependencies missing %q; got %+v", want, e.Dependencies)
		}
	}
}

func TestParseManifestFullSuccess(t *testing.T) {
	e := ParseManifest("amd64_022bd292..._4.0.15912.251_....manifest", fixtureComponentManifest, fixtureDictionary)
	if e.Err != nil {
		t.Fatalf("ParseManifest: %v", e.Err)
	}
	if e.Kind != KindComponent {
		t.Errorf("Kind = %v, want KindComponent", e.Kind)
	}
	if e.Identity.Name != "022bd29263008e5688235b714058746f" {
		t.Errorf("Identity.Name = %q", e.Identity.Name)
	}
	if len(e.Dependencies) != 1 || e.Dependencies[0].Name != "System" {
		t.Errorf("Dependencies = %+v, want [{Name: System}]", e.Dependencies)
	}
}

func TestParseManifestMissingDictionaryFails(t *testing.T) {
	e := ParseManifest("x.manifest", fixtureComponentManifest, nil)
	if e.Err == nil {
		t.Fatal("expected an error decoding without the shared dictionary")
	}
	if e.Manifest != nil {
		t.Error("Manifest should be nil on failure")
	}
}

func TestStoreQueries(t *testing.T) {
	entries := []*Entry{
		ParseMUM("HyperV-Compute-Host-Package.mum", fixturePackageWithUpdates),
		ParseMUM("KB5030219.mum", fixtureKBWrapper),
		ParseManifest("component.manifest", fixtureComponentManifest, fixtureDictionary),
		{Kind: KindComponent, FileName: "broken.manifest", Err: errTest},
	}
	s := Build(entries)

	if len(s.Entries) != 4 {
		t.Fatalf("len(Entries) = %d, want 4", len(s.Entries))
	}

	t.Run("ByName exact", func(t *testing.T) {
		got := s.ByName("HyperV-Compute-Host-Package")
		if len(got) != 1 || got[0].FileName != "HyperV-Compute-Host-Package.mum" {
			t.Errorf("ByName exact = %+v", got)
		}
	})

	t.Run("ByName glob", func(t *testing.T) {
		got := s.ByName("HyperV-*")
		if len(got) != 1 {
			t.Errorf("ByName glob = %+v, want 1 match", got)
		}
	})

	t.Run("Lookup", func(t *testing.T) {
		got := s.Lookup("022bd29263008e5688235b714058746f")
		if len(got) != 1 || got[0].Kind != KindComponent {
			t.Errorf("Lookup = %+v", got)
		}
	})

	t.Run("ByArchitecture", func(t *testing.T) {
		got := s.ByArchitecture("amd64")
		// HyperV-Compute-Host-Package and the component manifest are both amd64;
		// KB5030219 (kb_wrapper) is also amd64 per its fixture -- all 3 successfully
		// parsed entries should match.
		if len(got) != 3 {
			t.Errorf("ByArchitecture(amd64) = %d entries, want 3: %+v", len(got), got)
		}
	})

	t.Run("ByKB", func(t *testing.T) {
		got := s.ByKB("KB5030219")
		if len(got) != 1 || got[0].FileName != "KB5030219.mum" {
			t.Errorf("ByKB = %+v", got)
		}
		if got2 := s.ByKB("KB0000000"); len(got2) != 0 {
			t.Errorf("ByKB(nonexistent) = %+v, want none", got2)
		}
	})

	t.Run("ResolveDependencies", func(t *testing.T) {
		pkg := s.ByName("HyperV-Compute-Host-Package")[0]
		resolved := s.ResolveDependencies(pkg)
		if len(resolved) != len(pkg.Dependencies) {
			t.Fatalf("len(resolved) = %d, want %d", len(resolved), len(pkg.Dependencies))
		}
		// None of HyperV-Compute-Host-Package's 4 dependencies are
		// themselves in this small fixture set, so every slot should
		// resolve to zero entries -- this exercises the "not found" path.
		for i, r := range resolved {
			if len(r) != 0 {
				t.Errorf("resolved[%d] = %+v, want none (dependency not in this Store)", i, r)
			}
		}
	})
}

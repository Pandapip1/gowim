package component

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/Pandapip1/gowim/mum"
	"github.com/Pandapip1/gowim/wim"
)

// fixturePlainManifest is a real, unmodified `WinSxS\Manifests` file taken
// verbatim from a real Windows 11 image (`install.wim` image 1, "Windows 11
// Home", build 10.0.26200, SP build 8037, en-US, amd64 -- the same image
// every measurement in install.go and installregistry.go cites). It is one
// of that image's 401 plain-XML manifests: it carries no `DCM\x01`/PA30
// layer at all, which is the exact shape Install writes.
//
// Two facts about it are asserted below against values read out of that same
// image's `COMPONENTS` hive, so this fixture is a real cross-check and not
// just a parseable blob:
//
//	keyform   amd64_microsoft.windows.common-controls_6595b64144ccf1df_5.82.26100.8037_none_87ebc5097a2f9e52
//	S256H     e2fa5a17662829b10f343428eac04e41bfafeac695da246297d9bb0655a72252
//	identity  Microsoft.Windows.Common-Controls, Culture=neutral, Type=win32, Version=5.82.26100.8037, PublicKeyToken=6595b64144ccf1df, ProcessorArchitecture=amd64
//	f!        f!comctl32.dll
//
//go:embed testdata/plain_common_controls.manifest
var fixturePlainManifest []byte

const (
	fixtureKeyForm  = "amd64_microsoft.windows.common-controls_6595b64144ccf1df_5.82.26100.8037_none_87ebc5097a2f9e52"
	fixtureS256H    = "e2fa5a17662829b10f343428eac04e41bfafeac695da246297d9bb0655a72252"
	fixtureIdentity = "Microsoft.Windows.Common-Controls, Culture=neutral, Type=win32, " +
		"Version=5.82.26100.8037, PublicKeyToken=6595b64144ccf1df, ProcessorArchitecture=amd64"
)

func newTestImage() (*wim.ImageMetadata, *wim.BlobTable) {
	return &wim.ImageMetadata{
		Root: &wim.DirEntry{
			Attributes: wim.FileAttributeDirectory,
			SecurityID: wim.SecurityIDNone,
		},
	}, &wim.BlobTable{}
}

func mustLookup(t *testing.T, root *wim.DirEntry, path string) *wim.DirEntry {
	t.Helper()
	e, err := root.Lookup(path)
	if err != nil {
		t.Fatalf("Lookup(%q): %v", path, err)
	}
	return e
}

// TestManifestHashMatchesRealHiveS256H is the load-bearing correctness claim
// behind the whole Serviceable path: that the `S256H` value CBS stores for a
// component is the SHA-256 of the manifest's content, which for a plain
// manifest is the SHA-256 of the file's own bytes. The constant compared
// against was read out of a real image's COMPONENTS hive, and the same
// equality holds for all 401 plain manifests in that image (measured with
// this repo's regf parser; zero exceptions).
func TestManifestHashMatchesRealHiveS256H(t *testing.T) {
	sum := ManifestHash(fixturePlainManifest)
	if got := hex.EncodeToString(sum[:]); got != fixtureS256H {
		t.Errorf("ManifestHash = %s, want the real hive's S256H %s", got, fixtureS256H)
	}
	// And the fixture really is plain, not PA30: Install would reject it
	// otherwise, and the whole point of the research pass was that plain is
	// allowed.
	if bytes.HasPrefix(fixturePlainManifest, []byte("DC")) {
		t.Fatal("fixture is not a plain-XML manifest")
	}
}

// TestCanonicalIdentityMatchesRealHive checks the synthesized `identity`
// string against the one a real image stores for this exact component,
// including the fixed, non-alphabetical field order and the Type= field that
// mum only started modeling for this purpose.
func TestCanonicalIdentityMatchesRealHive(t *testing.T) {
	m, err := mum.Parse(fixturePlainManifest)
	if err != nil {
		t.Fatalf("mum.Parse: %v", err)
	}
	if got := CanonicalIdentity(m.Identity); got != fixtureIdentity {
		t.Errorf("CanonicalIdentity =\n  %q\nwant the real hive's identity value\n  %q", got, fixtureIdentity)
	}
}

func TestCanonicalIdentityOptionalFields(t *testing.T) {
	// Shapes taken from the four orderings measured across all 28069
	// identity values in the real image.
	tests := []struct {
		name string
		id   mum.AssemblyIdentity
		want string
	}{
		{
			name: "nonSxS servicing component, no Type",
			id: mum.AssemblyIdentity{
				Name: "Microsoft-Windows-CoreOS", Version: "10.0.26100.4202",
				PublicKeyToken: "31bf3856ad364e35", ProcessorArchitecture: "amd64",
				VersionScope: "NonSxS",
			},
			want: "Microsoft-Windows-CoreOS, Culture=neutral, Version=10.0.26100.4202, " +
				"PublicKeyToken=31bf3856ad364e35, ProcessorArchitecture=amd64, versionScope=NonSxS",
		},
		{
			name: "language becomes Culture",
			id: mum.AssemblyIdentity{
				Name: "Microsoft.WSMan.Management.Resources", Version: "1.0.0.0", Language: "en-US",
				PublicKeyToken: "31bf3856ad364e35", ProcessorArchitecture: "msil",
			},
			want: "Microsoft.WSMan.Management.Resources, Culture=en-US, Version=1.0.0.0, " +
				"PublicKeyToken=31bf3856ad364e35, ProcessorArchitecture=msil",
		},
		{
			name: "buildType is never canonicalized",
			id: mum.AssemblyIdentity{
				Name: "X", Version: "1.0.0.0", BuildType: "release",
				PublicKeyToken: "0000000000000000", ProcessorArchitecture: "amd64",
			},
			want: "X, Culture=neutral, Version=1.0.0.0, PublicKeyToken=0000000000000000, ProcessorArchitecture=amd64",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanonicalIdentity(tc.id); got != tc.want {
				t.Errorf("CanonicalIdentity = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDeploymentKeyNamePrefix checks the 11+".."+11 truncation against real
// deployment key names read out of the image (the full rule was verified
// against all 3983 of them; these three are representative: one truncated
// 32-hex name, one truncated long name, one short enough to pass through).
func TestDeploymentKeyNamePrefix(t *testing.T) {
	tests := []struct{ in, want string }{
		{"0074283ce7ee52524aaa00edcf95464e", "0074283ce7e..0edcf95464e"},
		{"microsoft-windows-client-desktop-required-deployment041130", "microsoft-w..yment041130"},
		{"Microsoft-Windows-CoreOS", "Microsoft-Windows-CoreOS"}, // exactly 24, untruncated
		{"short", "short"},
	}
	for _, tc := range tests {
		if got := DeploymentKeyNamePrefix(tc.in); got != tc.want {
			t.Errorf("DeploymentKeyNamePrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if len(tc.want) > 24 {
			t.Errorf("test bug: %q exceeds the measured 24-character cap", tc.want)
		}
	}
}

func TestCatalogThumbprint(t *testing.T) {
	data := []byte("not really a catalog")
	sum := sha256.Sum256(data)
	if got, want := CatalogThumbprint(data), hex.EncodeToString(sum[:]); got != want {
		t.Errorf("CatalogThumbprint = %q, want %q", got, want)
	}
}

func TestInstallRequiresServiceabilityChoice(t *testing.T) {
	md, bt := newTestImage()
	_, _, err := Install(md, bt, &Installation{
		Components: []ComponentInstall{{KeyForm: fixtureKeyForm, Manifest: fixturePlainManifest}},
	})
	if !errors.Is(err, ErrServiceabilityUnset) {
		t.Fatalf("Install with zero Serviceability: err = %v, want ErrServiceabilityUnset", err)
	}
}

func TestInstallPlacesFiles(t *testing.T) {
	md, bt := newTestImage()
	payload := []byte("payload bytes")

	root, newBlobs, err := Install(md, bt, &Installation{
		Serviceability: BuildOnce,
		Components: []ComponentInstall{{
			KeyForm:  fixtureKeyForm,
			Manifest: fixturePlainManifest,
			Files: []PayloadFile{{
				Name:     "comctl32.dll",
				Data:     payload,
				DestDirs: []string{`Windows\System32`, "Windows/SysWOW64"},
			}},
		}},
		Packages: []PackageInstall{{
			Name:    "Test-Package~31bf3856ad364e35~amd64~~1.0.0.0",
			MUM:     []byte("<assembly xmlns=\"urn:schemas-microsoft-com:asm.v3\"/>"),
			Catalog: []byte("catalog bytes"),
		}},
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	thumb := CatalogThumbprint([]byte("catalog bytes"))
	for _, path := range []string{
		ManifestsDir + `\` + fixtureKeyForm + ".manifest",
		WinSxSDir + `\` + fixtureKeyForm + `\comctl32.dll`,
		`Windows\System32\comctl32.dll`,
		`Windows\SysWOW64\comctl32.dll`, // '/' separator normalized
		PackagesDir + `\Test-Package~31bf3856ad364e35~amd64~~1.0.0.0.mum`,
		PackagesDir + `\Test-Package~31bf3856ad364e35~amd64~~1.0.0.0.cat`,
		CatalogsDir + `\` + thumb + ".cat",
	} {
		mustLookup(t, root, path)
	}

	// The store copy and the two projections must share one blob, refcounted
	// three times -- the offline analogue of the hardlinks CBS uses.
	payloadHash := wim.Hash(sha1.Sum(payload))
	var found bool
	for _, e := range bt.Entries {
		if e.Hash == payloadHash {
			found = true
			if e.RefCount != 3 {
				t.Errorf("payload RefCount = %d, want 3 (WinSxS store + System32 + SysWOW64)", e.RefCount)
			}
		}
	}
	if !found {
		t.Fatal("payload blob not added to the blob table")
	}

	// The catalog goes to two paths but is one blob.
	catHash := wim.Hash(sha1.Sum([]byte("catalog bytes")))
	for _, e := range bt.Entries {
		if e.Hash == catHash && e.RefCount != 2 {
			t.Errorf("catalog RefCount = %d, want 2 (servicing\\Packages + WinSxS\\Catalogs)", e.RefCount)
		}
	}

	// 4 distinct contents: manifest, payload, mum, catalog.
	if len(newBlobs) != 4 {
		t.Errorf("len(newBlobs) = %d, want 4", len(newBlobs))
	}
}

func TestInstallNoPayloadDirectoryWhenNoFiles(t *testing.T) {
	// 6894 of 28069 real components have no WinSxS payload directory at all;
	// Install must reproduce that rather than creating an empty one.
	md, bt := newTestImage()
	root, _, err := Install(md, bt, &Installation{
		Serviceability: BuildOnce,
		Components:     []ComponentInstall{{KeyForm: fixtureKeyForm, Manifest: fixturePlainManifest}},
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := root.Lookup(WinSxSDir + `\` + fixtureKeyForm); !errors.Is(err, wim.ErrNotFound) {
		t.Errorf("payload directory exists for a component with no files (err = %v)", err)
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	md, bt := newTestImage()
	inst := &Installation{
		Serviceability: BuildOnce,
		Components: []ComponentInstall{{
			KeyForm:  fixtureKeyForm,
			Manifest: fixturePlainManifest,
			Files:    []PayloadFile{{Name: "comctl32.dll", Data: []byte("x"), DestDirs: []string{`Windows\System32`}}},
		}},
	}
	if _, _, err := Install(md, bt, inst); err != nil {
		t.Fatalf("Install #1: %v", err)
	}
	before := len(bt.Entries)
	refs := map[wim.Hash]uint32{}
	for _, e := range bt.Entries {
		refs[e.Hash] = e.RefCount
	}
	_, newBlobs, err := Install(md, bt, inst)
	if err != nil {
		t.Fatalf("Install #2: %v", err)
	}
	if len(bt.Entries) != before {
		t.Errorf("second Install added %d blob-table entries, want 0", len(bt.Entries)-before)
	}
	if len(newBlobs) != 0 {
		t.Errorf("second Install reported %d new blobs, want 0", len(newBlobs))
	}
	for _, e := range bt.Entries {
		if e.RefCount != refs[e.Hash] {
			t.Errorf("blob %x RefCount %d -> %d across a repeated Install", e.Hash[:4], refs[e.Hash], e.RefCount)
		}
	}
	// Same tree, no duplicate children.
	manifests, err := md.Root.ReadDir(ManifestsDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(manifests) != 1 {
		t.Errorf("Manifests has %d children after two Installs, want 1", len(manifests))
	}
}

func TestInstallRejectsCompressedManifest(t *testing.T) {
	md, bt := newTestImage()
	_, _, err := Install(md, bt, &Installation{
		Serviceability: BuildOnce,
		Components:     []ComponentInstall{{KeyForm: fixtureKeyForm, Manifest: []byte("DCM\x01whatever")}},
	})
	if err == nil {
		t.Fatal("Install accepted a DCM/PA30-prefixed manifest")
	}
}

func TestInstallValidation(t *testing.T) {
	tests := []struct {
		name string
		inst *Installation
	}{
		{"no keyform", &Installation{Serviceability: BuildOnce,
			Components: []ComponentInstall{{Manifest: fixturePlainManifest}}}},
		{"keyform with separator", &Installation{Serviceability: BuildOnce,
			Components: []ComponentInstall{{KeyForm: `a\b`, Manifest: fixturePlainManifest}}}},
		{"no manifest", &Installation{Serviceability: BuildOnce,
			Components: []ComponentInstall{{KeyForm: fixtureKeyForm}}}},
		{"payload name with separator", &Installation{Serviceability: BuildOnce,
			Components: []ComponentInstall{{KeyForm: fixtureKeyForm, Manifest: fixturePlainManifest,
				Files: []PayloadFile{{Name: `sub\x.dll`, Data: []byte("x")}}}}}},
		{"package without mum", &Installation{Serviceability: BuildOnce,
			Packages: []PackageInstall{{Name: "P"}}}},
		{"bad thumbprint", &Installation{Serviceability: BuildOnce,
			Deployments: []DeploymentInstall{{KeyName: "d", CatalogThumbprint: "nope"}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			md, bt := newTestImage()
			if _, _, err := Install(md, bt, tc.inst); err == nil {
				t.Error("Install accepted an invalid Installation")
			}
		})
	}
}

// TestInstallThenRemove checks the one direction of round-trip that is
// actually an inverse, and documents the one that is not.
func TestInstallThenRemove(t *testing.T) {
	md, bt := newTestImage()
	root, _, err := Install(md, bt, &Installation{
		Serviceability: BuildOnce,
		Components: []ComponentInstall{{
			KeyForm:  fixtureKeyForm,
			Manifest: fixturePlainManifest,
			Files:    []PayloadFile{{Name: "comctl32.dll", Data: []byte("payload")}},
		}},
		Packages: []PackageInstall{{
			Name:    "Test-Package~31bf3856ad364e35~amd64~~1.0.0.0",
			MUM:     []byte("<assembly xmlns=\"urn:schemas-microsoft-com:asm.v3\"/>"),
			Catalog: []byte("catalog bytes"),
		}},
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if err := Remove(root, bt, &Entry{Kind: KindComponent, FileName: fixtureKeyForm + ".manifest"}); err != nil {
		t.Fatalf("Remove component: %v", err)
	}
	if err := Remove(root, bt, &Entry{Kind: KindPackage,
		FileName: "Test-Package~31bf3856ad364e35~amd64~~1.0.0.0.mum"}); err != nil {
		t.Fatalf("Remove package: %v", err)
	}

	for _, path := range []string{
		ManifestsDir + `\` + fixtureKeyForm + ".manifest",
		WinSxSDir + `\` + fixtureKeyForm,
		PackagesDir + `\Test-Package~31bf3856ad364e35~amd64~~1.0.0.0.mum`,
		PackagesDir + `\Test-Package~31bf3856ad364e35~amd64~~1.0.0.0.cat`,
	} {
		if _, err := root.Lookup(path); !errors.Is(err, wim.ErrNotFound) {
			t.Errorf("Remove left %q behind (err = %v)", path, err)
		}
	}
	catHash := wim.Hash(sha1.Sum([]byte("catalog bytes")))
	for _, e := range bt.Entries {
		want := uint32(0)
		if e.Hash == catHash {
			// Still referenced by the WinSxS\Catalogs copy; see below.
			want = 1
		}
		if e.RefCount != want {
			t.Errorf("blob %x has RefCount %d after Remove, want %d", e.Hash[:4], e.RefCount, want)
		}
	}

	// Not an inverse, and deliberately so: Remove predates Install and is
	// scoped to the two file sets a `.mum`/`.manifest` Entry names. It does
	// not know about the WinSxS\Catalogs copy Install also writes, nor about
	// payload projected into System32 and friends -- Remove takes an *Entry
	// (a parsed manifest), which carries no record of where a payload was
	// projected. Asserted here so the asymmetry is a tested, documented fact
	// rather than a surprise.
	thumb := CatalogThumbprint([]byte("catalog bytes"))
	if _, err := root.Lookup(CatalogsDir + `\` + thumb + ".cat"); err != nil {
		t.Errorf("expected Remove to leave the WinSxS\\Catalogs copy behind, but Lookup failed: %v", err)
	}
}

func TestInstallationTouchesFileMaps(t *testing.T) {
	storeOnly := &Installation{Serviceability: BuildOnce, Components: []ComponentInstall{{
		KeyForm: fixtureKeyForm, Manifest: fixturePlainManifest,
		Files: []PayloadFile{{Name: "comctl32.dll", Data: []byte("x")}},
	}}}
	if InstallationTouchesFileMaps(storeOnly) {
		t.Error("a store-only component should not be reported as touching FileMaps")
	}
	projected := &Installation{Serviceability: BuildOnce, Components: []ComponentInstall{{
		KeyForm: fixtureKeyForm, Manifest: fixturePlainManifest,
		Files: []PayloadFile{{Name: "x.dll", Data: []byte("x"), DestDirs: []string{`Windows\System32`}}},
	}}}
	if !InstallationTouchesFileMaps(projected) {
		t.Error("a component projecting payload into System32 should be reported as touching FileMaps")
	}
}

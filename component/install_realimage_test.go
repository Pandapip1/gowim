package component

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Pandapip1/gowim/mum"
	"github.com/Pandapip1/gowim/pa30"
	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/wim"
)

// The tests in this file run against a real, unmodified Windows installation
// image rather than a fixture, because the schema this package writes is
// entirely undocumented: the only thing that can say whether
// CanonicalIdentity, ManifestHash, the `f!` naming rule and the deployment
// key-name truncation are right is a real image's own `COMPONENTS` hive.
// They are the same measurements install.go and installregistry.go cite in
// their doc comments, re-run as assertions.
//
// Point GOWIM_TEST_IMAGE at a Windows `install.wim` (or any WIM whose first
// image is a full Windows installation) to enable them; they skip otherwise,
// since a 7.5 GB image is not something to check in. GOWIM_TEST_IMAGE_INDEX
// selects an image other than the first.
//
// Nothing is written to the image: every test copies the parsed metadata's
// mutable parts or works on the in-memory hive tree only.
func realImagePath(t *testing.T) string {
	t.Helper()
	p := os.Getenv("GOWIM_TEST_IMAGE")
	if p == "" {
		t.Skip("GOWIM_TEST_IMAGE not set; skipping real-image validation " +
			"(set it to a Windows install.wim to run these)")
	}
	return p
}

type realImage struct {
	r    *wim.Reader
	md   *wim.ImageMetadata
	bt   *wim.BlobTable
	hive *regf.Hive
	f    *os.File
}

func openRealImage(t *testing.T) *realImage {
	t.Helper()
	path := realImagePath(t)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { f.Close() })
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	r, err := wim.NewReader(f, fi.Size())
	if err != nil {
		t.Fatalf("wim.NewReader: %v", err)
	}
	bt, err := r.BlobTable()
	if err != nil {
		t.Fatalf("BlobTable: %v", err)
	}
	metas := bt.MetadataResources()
	idx := 0
	if s := os.Getenv("GOWIM_TEST_IMAGE_INDEX"); s != "" {
		if _, err := fmtSscan(s, &idx); err != nil {
			t.Fatalf("GOWIM_TEST_IMAGE_INDEX: %v", err)
		}
		idx--
	}
	if idx < 0 || idx >= len(metas) {
		t.Fatalf("image index out of range: %d of %d", idx+1, len(metas))
	}
	md, err := r.ImageMetadata(metas[idx])
	if err != nil {
		t.Fatalf("ImageMetadata: %v", err)
	}

	hiveBytes, err := r.ReadFile(md.Root, bt, `Windows\System32\config\COMPONENTS`)
	if err != nil {
		t.Skipf("image has no readable COMPONENTS hive (%v); not a full Windows installation image", err)
	}
	hive, err := regf.Parse(hiveBytes)
	if err != nil {
		t.Fatalf("regf.Parse(COMPONENTS): %v", err)
	}
	return &realImage{r: r, md: md, bt: bt, hive: hive, f: f}
}

// fmtSscan is a tiny wrapper so this file does not import fmt only for one
// Sscan call in an env-var path.
func fmtSscan(s string, n *int) (int, error) {
	v := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("not a number: " + s)
		}
		v = v*10 + int(c-'0')
	}
	*n = v
	return 1, nil
}

// TestRealImageManifestInvariants re-derives, from the image itself, the two
// invariants the Serviceable path depends on:
//
//   - `DerivedData\Components` is 1:1 with `WinSxS\Manifests\*.manifest` by
//     name, so a KeyForm names both.
//   - `S256H` is the SHA-256 of the manifest's *content* -- of the raw file
//     bytes for the plain manifests this package writes, and of the
//     PA30-decompressed XML for the rest.
func TestRealImageManifestInvariants(t *testing.T) {
	img := openRealImage(t)

	manDir, err := img.md.Root.ReadDir(ManifestsDir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", ManifestsDir, err)
	}
	comps := img.hive.Root.OpenPath(ComponentsKeyPath)
	if comps == nil {
		t.Fatalf("hive has no %s key", ComponentsKeyPath)
	}

	fileNames := map[string]bool{}
	for _, c := range manDir {
		n := c.NameUTF8()
		if c.IsDirectory() || !strings.HasSuffix(strings.ToLower(n), ".manifest") {
			continue
		}
		fileNames[strings.ToLower(trimSuffixFold(n, ".manifest"))] = true
	}
	keyNames := map[string]bool{}
	for _, k := range comps.Subkeys {
		keyNames[strings.ToLower(k.NameUTF8())] = true
	}
	if len(fileNames) != len(keyNames) {
		t.Errorf("%d manifests vs %d %s keys", len(fileNames), len(keyNames), ComponentsKeyPath)
	}
	var onlyFile, onlyKey int
	for n := range fileNames {
		if !keyNames[n] {
			onlyFile++
		}
	}
	for n := range keyNames {
		if !fileNames[n] {
			onlyKey++
		}
	}
	if onlyFile != 0 || onlyKey != 0 {
		t.Errorf("1:1 invariant broken: %d manifests with no key, %d keys with no manifest", onlyFile, onlyKey)
	}
	t.Logf("manifest/component-key 1:1 verified over %d entries", len(fileNames))

	// S256H, and the canonical identity, over every manifest in the image.
	dict, err := os.ReadFile("testdata/wcp_dictionary.bin")
	if err != nil {
		t.Fatalf("read dictionary: %v", err)
	}
	var checkedHash, checkedIdentity, plain int
	for _, k := range comps.Subkeys {
		name := k.NameUTF8()
		data, err := img.r.ReadFile(img.md.Root, img.bt, ManifestsDir+`\`+name+".manifest")
		if err != nil {
			t.Fatalf("read manifest %s: %v", name, err)
		}
		isPlain := !(len(data) >= 3 && string(data[:3]) == "DCM")
		if isPlain {
			plain++
			// The exact shape Install writes: hash of the raw file bytes.
			sum := ManifestHash(data)
			if got, want := hex.EncodeToString(sum[:]), hex.EncodeToString(k.Value("S256H").Data); got != want {
				t.Fatalf("%s: ManifestHash(raw plain bytes) = %s, hive S256H = %s", name, got, want)
			}
			checkedHash++
		}

		e := ParseManifest(name+".manifest", data, dict)
		if e.Err != nil {
			t.Fatalf("ParseManifest %s: %v", name, e.Err)
		}
		if !isPlain {
			// For a compressed manifest the same rule holds against the
			// decompressed content, which is what makes "content, not file
			// bytes" the right description of S256H.
			xmlData, err := reDecode(data, dict)
			if err != nil {
				t.Fatalf("decode %s: %v", name, err)
			}
			sum := sha256.Sum256(xmlData)
			if got, want := hex.EncodeToString(sum[:]), hex.EncodeToString(k.Value("S256H").Data); got != want {
				t.Fatalf("%s: SHA-256(decompressed XML) = %s, hive S256H = %s", name, got, want)
			}
			checkedHash++
		}

		if got, want := CanonicalIdentity(e.Identity), string(k.Value("identity").Data); got != want {
			t.Fatalf("%s: CanonicalIdentity =\n  %q\nhive identity =\n  %q", name, got, want)
		}
		checkedIdentity++
	}
	t.Logf("S256H verified for %d manifests (%d of them plain XML); "+
		"CanonicalIdentity verified against the hive for %d", checkedHash, plain, checkedIdentity)
}

// TestRealImageFileValueNames checks the `f!` naming rule this package
// enforces -- verbatim up to 25 characters, never verbatim beyond -- against
// every component in the image that has both payload files in its manifest
// and `f!` values in its hive key.
func TestRealImageFileValueNames(t *testing.T) {
	img := openRealImage(t)
	dict, err := os.ReadFile("testdata/wcp_dictionary.bin")
	if err != nil {
		t.Fatalf("read dictionary: %v", err)
	}
	comps := img.hive.Root.OpenPath(ComponentsKeyPath)

	var verbatim, truncated, longestVerbatim int
	var shortestTruncated = 1 << 30
	for _, k := range comps.Subkeys {
		hive := map[string]bool{}
		for i := range k.Values {
			n := k.Values[i].NameUTF8()
			if strings.HasPrefix(n, "f!") {
				hive[strings.ToLower(n[2:])] = true
			}
		}
		if len(hive) == 0 {
			continue
		}
		name := k.NameUTF8()
		data, err := img.r.ReadFile(img.md.Root, img.bt, ManifestsDir+`\`+name+".manifest")
		if err != nil {
			t.Fatalf("read manifest %s: %v", name, err)
		}
		xmlData := data
		if len(data) >= 3 && string(data[:3]) == "DCM" {
			if xmlData, err = reDecode(data, dict); err != nil {
				t.Fatalf("decode %s: %v", name, err)
			}
		}
		for _, f := range manifestFileNames(xmlData) {
			if hive[strings.ToLower(f)] {
				verbatim++
				if len(f) > longestVerbatim {
					longestVerbatim = len(f)
				}
			} else if len(f) > maxVerbatimFileValueName {
				truncated++
				if len(f) < shortestTruncated {
					shortestTruncated = len(f)
				}
			}
		}
	}
	if verbatim == 0 {
		t.Fatal("no verbatim f! names found at all; the pairing logic is wrong, not the rule")
	}
	if longestVerbatim > maxVerbatimFileValueName {
		t.Errorf("found a verbatim f! name of %d characters, past the assumed %d-character boundary",
			longestVerbatim, maxVerbatimFileValueName)
	}
	if _, err := fileValueName(strings.Repeat("a", longestVerbatim)); err != nil {
		t.Errorf("fileValueName rejects a length (%d) that really does occur verbatim: %v", longestVerbatim, err)
	}
	t.Logf("f! names: %d verbatim (longest %d chars), %d truncated (shortest source name %d chars); "+
		"package boundary is %d", verbatim, longestVerbatim, truncated, shortestTruncated, maxVerbatimFileValueName)
}

// TestRealImageDeploymentKeyNames checks DeploymentKeyNamePrefix against
// every deployment key in the image, deriving the expected prefix from that
// key's own `appid` value.
func TestRealImageDeploymentKeyNames(t *testing.T) {
	img := openRealImage(t)
	deps := img.hive.Root.OpenPath(DeploymentsKeyPath)
	if deps == nil {
		t.Fatalf("hive has no %s key", DeploymentsKeyPath)
	}
	var checked, truncated int
	for _, d := range deps.Subkeys {
		key := d.NameUTF8()
		v := d.Value("appid")
		if v == nil {
			t.Errorf("deployment %s has no appid", key)
			continue
		}
		appid := string(v.Data)
		name := appid
		if i := strings.Index(appid, ","); i >= 0 {
			name = appid[:i]
		}
		parts := strings.Split(key, "_")
		if len(parts) < 4 {
			t.Errorf("deployment key %q has fewer than four `_`-separated fields", key)
			continue
		}
		got := strings.Join(parts[:len(parts)-3], "_")
		want := DeploymentKeyNamePrefix(name)
		if !strings.EqualFold(got, want) {
			t.Fatalf("deployment %q: key name field = %q, DeploymentKeyNamePrefix(%q) = %q", key, got, name, want)
		}
		if strings.Contains(want, "..") {
			truncated++
		}
		checked++
	}
	t.Logf("DeploymentKeyNamePrefix verified against %d real deployment keys (%d of them truncated)",
		checked, truncated)
}

// TestRealImageCatalogInvariant checks that a catalog's key name and file
// name really are the SHA-256 of its own bytes, which is what
// CatalogThumbprint computes and what Install uses to name the
// `WinSxS\Catalogs` copy.
func TestRealImageCatalogInvariant(t *testing.T) {
	img := openRealImage(t)
	cats := img.hive.Root.OpenPath(CatalogsKeyPath)
	if cats == nil {
		t.Fatalf("hive has no %s key", CatalogsKeyPath)
	}
	catDir, err := img.md.Root.ReadDir(CatalogsDir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", CatalogsDir, err)
	}
	files := map[string]bool{}
	for _, c := range catDir {
		n := strings.ToLower(c.NameUTF8())
		if strings.HasSuffix(n, ".cat") {
			files[trimSuffixFold(n, ".cat")] = true
		}
	}
	if len(files) != len(cats.Subkeys) {
		t.Errorf("%d WinSxS\\Catalogs files vs %d %s keys", len(files), len(cats.Subkeys), CatalogsKeyPath)
	}
	// Hashing all of them is a few hundred MB of reads; a bounded sample is
	// enough to establish the naming rule, and the set equality above
	// already covers the 1:1 half.
	var checked int
	for _, k := range cats.Subkeys {
		name := strings.ToLower(k.NameUTF8())
		if !files[name] {
			t.Errorf("catalog key %q has no WinSxS\\Catalogs file", name)
			continue
		}
		data, err := img.r.ReadFile(img.md.Root, img.bt, CatalogsDir+`\`+k.NameUTF8()+".cat")
		if err != nil {
			t.Fatalf("read catalog %s: %v", name, err)
		}
		if got := CatalogThumbprint(data); got != name {
			t.Fatalf("catalog %q: CatalogThumbprint = %s", name, got)
		}
		checked++
		if checked >= 64 {
			break
		}
	}
	t.Logf("catalog naming verified: %d files/keys 1:1, %d hashed", len(files), checked)
}

// TestRealImageInstallRoundTrip is the end-to-end exercise: install a
// component into a real offline image's real directory tree and real
// `COMPONENTS` hive, check the result against the shape a real neighbouring
// component has, then Remove it and check the tree is back where it started.
//
// It mutates only in-memory structures parsed out of the image; the image
// file is opened read-only and never written back. What it therefore does
// *not* prove is that Windows accepts the result -- no live end-to-end
// confirmation of that exists yet (see TODO.md).
func TestRealImageInstallRoundTrip(t *testing.T) {
	img := openRealImage(t)

	// A keyform that cannot collide with a real one, but shaped like one.
	const keyForm = "amd64_gowim.test.component_0000000000000000_1.0.0.0_none_0000000000000000"
	manifestXML, err := (&mum.Manifest{
		ManifestVersion: "1.0",
		Identity: mum.AssemblyIdentity{
			Name: "Gowim.Test.Component", Version: "1.0.0.0",
			ProcessorArchitecture: "amd64", PublicKeyToken: "0000000000000000",
			Type: "win32",
		},
	}).Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	payload := []byte("gowim test payload\n")
	inst := &Installation{
		Serviceability: Serviceable,
		Components: []ComponentInstall{{
			KeyForm:  keyForm,
			Manifest: manifestXML,
			Files:    []PayloadFile{{Name: "gowimtest.dll", Data: payload, DestDirs: []string{`Windows\System32`}}},
		}},
	}

	manifestsBefore, err := img.md.Root.ReadDir(ManifestsDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	nBefore := len(manifestsBefore)
	blobsBefore := len(img.bt.Entries)

	root, newBlobs, err := Install(img.md, img.bt, inst)
	if err != nil {
		t.Fatalf("Install into real image: %v", err)
	}
	if len(newBlobs) != 2 {
		t.Errorf("len(newBlobs) = %d, want 2 (manifest + payload)", len(newBlobs))
	}
	if len(img.bt.Entries) != blobsBefore+2 {
		t.Errorf("blob table grew by %d, want 2", len(img.bt.Entries)-blobsBefore)
	}
	for _, p := range []string{
		ManifestsDir + `\` + keyForm + ".manifest",
		WinSxSDir + `\` + keyForm + `\gowimtest.dll`,
		`Windows\System32\gowimtest.dll`,
	} {
		if _, err := root.Lookup(p); err != nil {
			t.Errorf("Lookup(%q) after Install: %v", p, err)
		}
	}
	if after, _ := root.ReadDir(ManifestsDir); len(after) != nBefore+1 {
		t.Errorf("Manifests directory grew by %d entries, want 1", len(after)-nBefore)
	}

	// Registry half, into the image's own parsed COMPONENTS hive.
	if err := InstallRegistry(&Hives{Components: img.hive.Root}, inst); err != nil {
		t.Fatalf("InstallRegistry into real hive: %v", err)
	}
	comps := img.hive.Root.OpenPath(ComponentsKeyPath)
	newKey := comps.Subkey(keyForm)
	if newKey == nil {
		t.Fatal("new component key not created in the real hive")
	}

	// The new key's value-name *shapes* must be a subset of what real
	// components in this very hive use -- that is the check that catches a
	// misnamed or invented value.
	realShapes := map[string]bool{}
	for _, k := range comps.Subkeys {
		if k == newKey {
			continue
		}
		for i := range k.Values {
			realShapes[valueShape(k.Values[i].NameUTF8())] = true
		}
	}
	var gotShapes []string
	for i := range newKey.Values {
		s := valueShape(newKey.Values[i].NameUTF8())
		gotShapes = append(gotShapes, s)
		if !realShapes[s] {
			t.Errorf("wrote value shape %q, which no real component in this image uses", s)
		}
	}
	sort.Strings(gotShapes)
	t.Logf("new component key value shapes: %v (all present in the real hive)", gotShapes)

	// And the value *types* must match what real components use for the same
	// names.
	typeOfShape := map[string]uint32{}
	for _, k := range comps.Subkeys {
		if k == newKey {
			continue
		}
		for i := range k.Values {
			typeOfShape[valueShape(k.Values[i].NameUTF8())] = k.Values[i].Type
		}
	}
	for i := range newKey.Values {
		s := valueShape(newKey.Values[i].NameUTF8())
		if want, ok := typeOfShape[s]; ok && newKey.Values[i].Type != want {
			t.Errorf("value %q written as type %d; real components use type %d",
				newKey.Values[i].NameUTF8(), newKey.Values[i].Type, want)
		}
	}

	// The identity we wrote must round-trip through CanonicalIdentity from
	// the manifest we wrote.
	m, err := mum.Parse(manifestXML)
	if err != nil {
		t.Fatalf("mum.Parse: %v", err)
	}
	if got, want := string(newKey.Value("identity").Data), CanonicalIdentity(m.Identity); got != want {
		t.Errorf("identity = %q, want %q", got, want)
	}

	// Now undo the file half and confirm the tree is back.
	if err := Remove(root, img.bt, &Entry{Kind: KindComponent, FileName: keyForm + ".manifest"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, p := range []string{
		ManifestsDir + `\` + keyForm + ".manifest",
		WinSxSDir + `\` + keyForm,
	} {
		if _, err := root.Lookup(p); !errors.Is(err, wim.ErrNotFound) {
			t.Errorf("Remove left %q behind (err = %v)", p, err)
		}
	}
	if after, _ := root.ReadDir(ManifestsDir); len(after) != nBefore {
		t.Errorf("Manifests directory has %d entries after Remove, want the original %d", len(after), nBefore)
	}
	// The projected System32 copy is *not* removed -- Remove works from a
	// manifest Entry, which records no projection. Asserted so the asymmetry
	// stays visible.
	if _, err := root.Lookup(`Windows\System32\gowimtest.dll`); err != nil {
		t.Errorf("expected the projected copy to survive Remove, but Lookup failed: %v", err)
	}
}

// reDecode decompresses a `DCM\x01`-prefixed manifest with the sibling pa30
// package, the same way ParseManifest does internally (ParseManifest returns
// only the parsed model, and these tests need the XML bytes themselves in
// order to hash them).
func reDecode(data, dict []byte) ([]byte, error) {
	out, _, err := pa30.DecodeWithSource(data[4:], dict)
	return out, err
}

// manifestFileNames extracts the `name` attribute of every `<file>` element
// in a manifest. The sibling mum package models a manifest's identity,
// package and dependency vocabulary but not its `<file>` elements (see
// PayloadFile), so these tests scan for them directly rather than adding a
// model this package's non-test code does not use.
var fileElementRe = regexp.MustCompile(`(?s)<file\s[^>]*?name="([^"]*)"`)

func manifestFileNames(xmlData []byte) []string {
	var out []string
	for _, m := range fileElementRe.FindAllSubmatch(xmlData, -1) {
		out = append(out, string(m[1]))
	}
	return out
}

// valueShape reduces a component value name to its kind, so `f!comctl32.dll`
// and `f!gdiplus.dll` compare equal while `identity` and `S256H` stay
// distinct.
func valueShape(name string) string {
	if len(name) > 2 && name[1] == '!' {
		return name[:2]
	}
	return name
}

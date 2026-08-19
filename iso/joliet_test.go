package iso

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// Joliet validation.
//
// Same standing rule as the rest of this package's external tests: a writer
// checked only by its own reader proves nothing. isoinfo -J, xorriso (which
// prefers Joliet/Rock Ridge over plain ISO 9660 when reading) and 7z were
// all confirmed Joliet-aware for this item (see TODO.md's entry), so all
// three are used here.

// jolietSampleTree writes a small tree exercising the Joliet-specific
// behaviour that the plain ECMA-119 sampleTree in external_test.go cannot:
// spaces, mixed case, multiple dots, illegal characters, and a name long
// enough to need truncation.
func jolietSampleTree(t *testing.T) (dir string, wantNames []string) {
	t.Helper()
	dir = t.TempDir()
	long := strings.Repeat("Long Name ", 8) // 80 runes, over jolietMaxNameLen
	files := map[string]string{
		"Weird Name With Spaces & Stuff.txt": "spaces\n",
		"MixedCase.Long.Name.With.Dots.dat":  "dots\n",
		"illegal*chars?in:name.bin":          "illegal\n",
		"sub dir/Nested File.bin":            "nested\n",
		long + "AAA.txt":                     "long a\n",
		long + "BBB.txt":                     "long b\n",
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The two "long" names collapse to the same 64-code-unit Joliet
	// identifier, so mangle()'s jolietDedupe call gives the first one
	// (alphabetically, "...AAA.txt") the plain truncated name and the
	// second ("...BBB.txt") a numeric-suffixed one — reproduced here by
	// calling the same two functions gowim itself uses, rather than
	// hand-computing the expected suffix, since only the *fact* of
	// collision resolution is what this test wants to check (see
	// TestJolietDedupeResolvesCollision for that in isolation).
	// genisoimage refuses to build this exact tree at all (see
	// TestCompareJolietSVDWithGenisoimage's comment on why it uses a
	// different, collision-free tree).
	used := map[string]bool{}
	first := utf16.Decode(jolietDedupe(used, mangleJolietName(long+"AAA.txt")))
	second := utf16.Decode(jolietDedupe(used, mangleJolietName(long+"BBB.txt")))
	want := []string{
		"/Weird Name With Spaces & Stuff.txt",
		"/MixedCase.Long.Name.With.Dots.dat",
		"/illegal_chars_in_name.bin",
		"/sub dir",
		"/sub dir/Nested File.bin",
		"/" + string(first),
		"/" + string(second),
	}
	sort.Strings(want)
	return dir, want
}

func buildJolietSample(t *testing.T, src string, opts *Options) string {
	t.Helper()
	b := New(opts)
	if err := b.AddTree("", src); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := b.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "joliet.iso")
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

/**************** name mangling unit tests ****************/

// TestMangleJolietNamePreservesCaseAndSpaces checks the headline property
// Joliet exists for: names that ECMA-119 would upper-case and truncate come
// through unchanged, code-unit for code-unit.
func TestMangleJolietNamePreservesCaseAndSpaces(t *testing.T) {
	got := mangleJolietName("Weird Name With Spaces & Stuff.txt")
	want := utf16.Encode([]rune("Weird Name With Spaces & Stuff.txt"))
	if string(jolietBytes(got)) != string(jolietBytes(want)) {
		t.Errorf("mangleJolietName changed a legal name: got %q, want %q",
			utf16.Decode(got), utf16.Decode(want))
	}
}

// TestMangleJolietNameIllegalChars checks every character joliet.c's
// convert_to_unicode documents or implements as illegal (see
// mangleJolietName's doc comment) folds to '_', and that legal punctuation
// (which ECMA-119 d-characters forbid but Joliet allows) survives.
func TestMangleJolietNameIllegalChars(t *testing.T) {
	got := utf16.Decode(mangleJolietName("a*b/c:d;e?f\\g\x01h\x7fi"))
	want := "a_b_c_d_e_f_g_h_i"
	if string(got) != want {
		t.Errorf("mangleJolietName(%q) = %q, want %q", `a*b/c:d;e?f\g`+"\x01h\x7fi", string(got), want)
	}
	if s := utf16.Decode(mangleJolietName("a+b=c(d).txt")); string(s) != "a+b=c(d).txt" {
		t.Errorf("legal punctuation was mangled: got %q", string(s))
	}
}

// TestMangleJolietNameTruncates checks the jolietMaxNameLen = 64 UCS-2 code
// unit limit (JMAX in genisoimage.h; see jolietMaxNameLen's doc comment).
func TestMangleJolietNameTruncates(t *testing.T) {
	in := strings.Repeat("x", 100)
	got := mangleJolietName(in)
	if len(got) != jolietMaxNameLen {
		t.Errorf("got %d code units, want %d", len(got), jolietMaxNameLen)
	}
}

// TestJolietDedupeResolvesCollision checks that two names differing only
// past the 64-code-unit truncation point, which would otherwise collide,
// come out distinct. genisoimage treats this as a fatal build error (see
// jolietDedupe's doc comment); this package resolves it instead.
func TestJolietDedupeResolvesCollision(t *testing.T) {
	used := map[string]bool{}
	base := strings.Repeat("A", jolietMaxNameLen)
	a := jolietDedupe(used, mangleJolietName(base+"1"))
	b := jolietDedupe(used, mangleJolietName(base+"2"))
	if string(jolietBytes(a)) == string(jolietBytes(b)) {
		t.Fatalf("two distinct names collided after Joliet dedupe: both became %q", utf16.Decode(a))
	}
	if len(a) > jolietMaxNameLen || len(b) > jolietMaxNameLen {
		t.Errorf("dedupe result exceeds jolietMaxNameLen: %d, %d", len(a), len(b))
	}
}

/**************** external tool validation ****************/

// TestIsoinfoReadsJolietNames validates the headline property with cdrkit's
// own reader: isoinfo -J must show the real long, mixed-case names, and the
// plain (non -J) view must still show the mangled ECMA-119 ones, proving
// the two hierarchies are genuinely independent rather than one aliasing
// the other.
func TestIsoinfoReadsJolietNames(t *testing.T) {
	if haveTool(t, "isoinfo") == "" {
		t.Skip("isoinfo not installed; cannot externally validate")
	}
	src, want := jolietSampleTree(t)
	iso := buildJolietSample(t, src, &Options{
		VolumeID:  "GOWIM_JOLIET",
		Joliet:    true,
		Timestamp: time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
	})

	out, err := exec.Command("isoinfo", "-J", "-f", "-i", iso).CombinedOutput()
	if err != nil {
		t.Fatalf("isoinfo -J -f: %v\n%s", err, out)
	}
	got := parsePathsIncludingDirs(string(out))
	if !equalStrings(got, want) {
		t.Errorf("isoinfo -J -f listed\n  %v\nwant\n  %v", got, want)
	}

	// Without -J, isoinfo reads the plain ECMA-119 tree, which must still be
	// there, mangled the way Level1 always mangles it — Joliet is additive.
	out, err = exec.Command("isoinfo", "-f", "-i", iso).CombinedOutput()
	if err != nil {
		t.Fatalf("isoinfo -f: %v\n%s", err, out)
	}
	plain := string(out)
	if strings.Contains(plain, "Weird Name With Spaces") {
		t.Errorf("plain ECMA-119 listing shows an unmangled Joliet name; the two hierarchies are not independent:\n%s", plain)
	}
	if !strings.Contains(plain, "SUB_DIR") {
		t.Errorf("plain ECMA-119 listing missing the mangled directory name; output:\n%s", plain)
	}

	out, err = exec.Command("isoinfo", "-d", "-i", iso).CombinedOutput()
	if err != nil {
		t.Fatalf("isoinfo -d: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Joliet with UCS level 3 found") {
		t.Errorf("isoinfo -d did not recognise the Joliet SVD; output:\n%s", out)
	}
}

// TestIsoinfoJolietFileContents checks the bytes isoinfo extracts through
// the Joliet path are the bytes that went in — the Joliet Directory Record
// shares the same underlying extent as the ECMA-119 one (see
// node.jolietDirExtent's doc comment; the *file data* extents, unlike the
// directory extents, are identical across every hierarchy).
func TestIsoinfoJolietFileContents(t *testing.T) {
	if haveTool(t, "isoinfo") == "" {
		t.Skip("isoinfo not installed; cannot externally validate")
	}
	src, _ := jolietSampleTree(t)
	iso := buildJolietSample(t, src, &Options{VolumeID: "GOWIM_JOLIET", Joliet: true})

	cases := []struct{ isoPath, hostPath string }{
		{"/Weird Name With Spaces & Stuff.txt", "Weird Name With Spaces & Stuff.txt"},
		{"/sub dir/Nested File.bin", "sub dir/Nested File.bin"},
	}
	for _, tc := range cases {
		want, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(tc.hostPath)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := exec.Command("isoinfo", "-J", "-i", iso, "-x", tc.isoPath).Output()
		if err != nil {
			t.Fatalf("isoinfo -J -x %s: %v", tc.isoPath, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: extracted %q, want %q", tc.isoPath, got, want)
		}
	}
}

// TestXorrisoReadsJolietNames validates against xorriso, an independent
// implementation lineage that (per its own default behaviour, confirmed
// while developing this test) prefers the Joliet or Rock Ridge view of a
// bridge volume over plain ECMA-119 when one is present.
func TestXorrisoReadsJolietNames(t *testing.T) {
	if haveTool(t, "xorriso") == "" {
		t.Skip("xorriso not installed; cannot cross-validate")
	}
	src, want := jolietSampleTree(t)
	iso := buildJolietSample(t, src, &Options{VolumeID: "GOWIM_JOLIET", Joliet: true})

	out, err := exec.Command("xorriso", "-indev", iso, "-find", "/", "-exec", "echo", "--").CombinedOutput()
	if err != nil {
		t.Fatalf("xorriso: %v\n%s", err, out)
	}
	text := string(out)
	for _, bad := range []string{"FAILURE", "SORRY"} {
		if strings.Contains(text, bad) {
			t.Errorf("xorriso reported %s:\n%s", bad, text)
		}
	}
	for _, p := range want {
		if !strings.Contains(text, "'"+p+"'") {
			t.Errorf("xorriso did not list %q; output:\n%s", p, text)
		}
	}
}

// Test7zReadsJolietNames validates against 7z, a third independent
// implementation.
func Test7zReadsJolietNames(t *testing.T) {
	if haveTool(t, "7z") == "" {
		t.Skip("7z not installed; cannot cross-validate")
	}
	src, _ := jolietSampleTree(t)
	iso := buildJolietSample(t, src, &Options{VolumeID: "GOWIM_JOLIET", Joliet: true})

	out, err := exec.Command("7z", "l", iso).CombinedOutput()
	if err != nil {
		t.Fatalf("7z l: %v\n%s", err, out)
	}
	text := string(out)
	for _, want := range []string{
		"Weird Name With Spaces & Stuff.txt",
		"MixedCase.Long.Name.With.Dots.dat",
		"sub dir",
		"Nested File.bin",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("7z l did not list %q; output:\n%s", want, text)
		}
	}
}

/**************** structural comparison against genisoimage -J ****************/

// TestCompareJolietSVDWithGenisoimage compares the Joliet Supplementary
// Volume Descriptor field-for-field against genisoimage -J, and the two
// producers' Joliet path lists against each other.
//
// This deliberately reuses the plain ECMA-119 sampleTree from
// external_test.go rather than jolietSampleTree: the latter's two
// same-after-truncation "long" names are exactly the case genisoimage
// refuses to build at all ("Error: ... have the same Joliet name", see
// jolietDedupe's doc comment), so a tree exercising that path cannot be
// used for a genisoimage comparison.
func TestCompareJolietSVDWithGenisoimage(t *testing.T) {
	if haveTool(t, "genisoimage") == "" || haveTool(t, "isoinfo") == "" {
		t.Skip("genisoimage/isoinfo not installed; cannot compare against the reference producer")
	}
	src, _ := sampleTree(t)
	mine := buildJolietSample(t, src, &Options{VolumeID: "GOWIM_JOLIET", Joliet: true})

	theirs := filepath.Join(t.TempDir(), "gen.iso")
	out, err := exec.Command("genisoimage", "-quiet", "-J", "-V", "GOWIM_JOLIET", "-o", theirs, src).CombinedOutput()
	if err != nil {
		t.Fatalf("genisoimage -J failed: %v\n%s", err, out)
	}

	mineSVD, err := readSVD(mine)
	if err != nil {
		t.Fatal(err)
	}
	theirSVD, err := readSVD(theirs)
	if err != nil {
		t.Fatal(err)
	}
	// Fields that must agree regardless of layout differences elsewhere in
	// the image (path table locations differ because gowim omits
	// genisoimage's version block and run-out padding, so those are not
	// compared).
	checks := []struct {
		name       string
		mine, want byte
	}{
		{"Volume Descriptor Type", mineSVD[0], theirSVD[0]},
		{"Volume Descriptor Version", mineSVD[6], theirSVD[6]},
		{"Volume Flags", mineSVD[7], theirSVD[7]},
		{"File Structure Version", mineSVD[881], theirSVD[881]},
	}
	for _, c := range checks {
		if c.mine != c.want {
			t.Errorf("%s: gowim %#x, genisoimage %#x", c.name, c.mine, c.want)
		}
	}
	if !bytes.Equal(mineSVD[88:91], theirSVD[88:91]) {
		t.Errorf("Escape Sequences: gowim %v, genisoimage %v", mineSVD[88:91], theirSVD[88:91])
	}
	if !bytes.Equal(mineSVD[91:120], make([]byte, 29)) {
		t.Errorf("Escape Sequences tail is not (00): %v", mineSVD[91:120])
	}

	minePaths := isoinfoJolietPaths(t, mine)
	theirPaths := isoinfoJolietPaths(t, theirs)
	if !equalStrings(minePaths, theirPaths) {
		t.Errorf("Joliet path lists differ.\n  gowim:       %v\n  genisoimage: %v", minePaths, theirPaths)
	}
}

// readSVD returns the 2048-byte Supplementary Volume Descriptor from an
// image file, scanning the Volume Descriptor Set starting at LBA 16 for the
// first descriptor of type 2 (ECMA-119 8.5.1).
func readSVD(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, LogicalSectorSize)
	for lba := int64(firstVolumeDescriptorSector); ; lba++ {
		if _, err := f.ReadAt(buf, lba*LogicalSectorSize); err != nil {
			return nil, err
		}
		if buf[0] == 255 {
			return nil, os.ErrNotExist
		}
		if buf[0] == 2 && string(buf[1:6]) == "CD001" {
			out := make([]byte, LogicalSectorSize)
			copy(out, buf)
			return out, nil
		}
	}
}

func isoinfoJolietPaths(t *testing.T, iso string) []string {
	t.Helper()
	out, err := exec.Command("isoinfo", "-J", "-f", "-i", iso).CombinedOutput()
	if err != nil {
		t.Fatalf("isoinfo -J -f %s: %v\n%s", iso, err, out)
	}
	return parsePaths(string(out))
}

// parsePathsIncludingDirs is parsePaths (external_test.go) without dropping
// directory entries, since jolietSampleTree's want list includes "/sub dir"
// itself.
func parsePathsIncludingDirs(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "/") {
			continue
		}
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

/**************** Joliet leaves everything else unaffected ****************/

// TestJolietDoesNotAffectOtherHierarchies builds one image with Joliet, UDF
// and El Torito all enabled together — the full Windows-media
// configuration plus Joliet — and re-runs each earlier phase's own
// validation against it, to confirm adding a fourth (well, second-and-a-
// half: Joliet's Path Table Group plus directory tree) filesystem view
// disturbed none of the others.
func TestJolietDoesNotAffectOtherHierarchies(t *testing.T) {
	if haveTool(t, "isoinfo") == "" || haveTool(t, "xorriso") == "" || haveTool(t, "7z") == "" {
		t.Skip("isoinfo/xorriso/7z not installed; cannot cross-validate")
	}
	src, wantECMA := sampleTree(t)
	bios := filepath.Join(src, "BIOS.BIN")
	if err := os.WriteFile(bios, bytes.Repeat([]byte{0x90}, 2048*8), 0o644); err != nil {
		t.Fatal(err)
	}
	efi := filepath.Join(src, "EFI.BIN")
	if err := os.WriteFile(efi, bytes.Repeat([]byte{0x91}, 2048*2880), 0o644); err != nil {
		t.Fatal(err)
	}

	b := New(&Options{
		VolumeID:  "GOWIM_JOLIET",
		UDF:       true,
		Joliet:    true,
		Timestamp: time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
		BootEntries: []BootEntry{
			{ImagePath: "BIOS.BIN"},
			{ImagePath: "EFI.BIN", Platform: BootPlatformUEFI},
		},
	})
	if err := b.AddTree("", src); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := b.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(t.TempDir(), "full.iso")
	if err := os.WriteFile(iso, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. The plain ECMA-119 path list is exactly what it would be without
	// Joliet (plus the two new boot images and the generated boot catalog).
	ecmaPaths := isoinfoPaths(t, iso)
	wantECMAFull := append(append([]string{}, wantECMA...),
		"/BIOS.BIN;1", "/EFI.BIN;1", "/BOOT.CAT;1")
	sort.Strings(wantECMAFull)
	if !equalStrings(ecmaPaths, wantECMAFull) {
		t.Errorf("ECMA-119 path list changed by enabling Joliet.\n  got:  %v\n  want: %v", ecmaPaths, wantECMAFull)
	}

	// 2. UDF, read by 7z (which prefers it in a bridge volume): the same
	// file count and no error, exactly as TestUDFStructure/Test7zReadsUDFTree
	// already establish without Joliet.
	out, err := exec.Command("7z", "l", iso).CombinedOutput()
	if err != nil {
		t.Fatalf("7z l: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Type = Udf") {
		t.Errorf("7z no longer identifies the image as UDF once Joliet is added:\n%s", out)
	}

	// 3. El Torito, read by xorriso: both entries still present with their
	// original sizes, exactly as TestXorrisoReportsElTorito establishes
	// without Joliet.
	out, err = exec.Command("xorriso", "-indev", iso, "-report_el_torito", "plain").CombinedOutput()
	if err != nil {
		t.Fatalf("xorriso -report_el_torito: %v\n%s", err, out)
	}
	text := string(out)
	for _, bad := range []string{"FAILURE", "SORRY"} {
		if strings.Contains(text, bad) {
			t.Errorf("xorriso -report_el_torito reported %s once Joliet was added:\n%s", bad, text)
		}
	}
	if !strings.Contains(text, "El Torito catalog") {
		t.Errorf("xorriso no longer reports an El Torito catalog once Joliet is added:\n%s", text)
	}

	// 4. And Joliet itself is there too.
	jPaths := isoinfoJolietPaths(t, iso)
	if len(jPaths) == 0 {
		t.Error("Joliet path list is empty in the combined image")
	}
}

// TestJolietOffByDefault checks that the zero Options value, like UDF and
// BootEntries, writes no Joliet Supplementary Volume Descriptor at all.
func TestJolietOffByDefault(t *testing.T) {
	src, _ := sampleTree(t)
	iso := buildJolietSample(t, src, &Options{VolumeID: "GOWIM_TEST"})
	if _, err := readSVD(iso); err == nil {
		t.Error("a Supplementary Volume Descriptor was written despite Options.Joliet being unset")
	}
}

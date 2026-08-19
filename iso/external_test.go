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
)

// External-tool validation.
//
// A writer that is only ever checked by its own reader proves nothing: a
// consistent misreading of the standard would pass every such test. These
// tests therefore hand the produced image to independently written ISO 9660
// readers and compare what they see against what was put in.
//
// The tools used, in order of preference, are:
//
//   - isoinfo, from cdrkit (the same package as genisoimage). It parses the
//     Primary Volume Descriptor and, with -f, walks the directory records.
//   - xorriso, from libburnia, an entirely separate implementation lineage.
//   - 7z, a third independent implementation.
//
// A test skips only if none of the tools it needs is installed, and says so
// loudly rather than passing silently.

func haveTool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// sampleTree writes a small directory tree to a temporary directory and
// returns its path, along with the set of ISO paths that should appear in
// the image (already mangled to what Level1 produces).
func sampleTree(t *testing.T) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"README.TXT":                 "hello iso 9660\n",
		"lower.txt":                  "lowercase name gets folded to upper case\n",
		"has-dash.bin":               strings.Repeat("\x00\x01\x02\x03", 1000),
		"sub/NESTED.DAT":             strings.Repeat("nested ", 500),
		"sub/deeper/DEEP.TXT":        "three levels down\n",
		"sub/deeper/empty.txt":       "",
		"another/spans_a_sector.bin": strings.Repeat("A", 5000),
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
	// What Level1 mangling (ECMA-119 10.1's 8.3 restriction) should turn
	// these into, complete with the ";1" File Version Number that 7.5.1
	// makes mandatory in a hierarchy identified by a Primary Volume
	// Descriptor. Note "spans_a_sector" truncating to "SPANS_A_" and the
	// '-' in "has-dash" folding to '_', neither being a d-character.
	want := []string{
		"/ANOTHER/SPANS_A_.BIN;1",
		"/HAS_DASH.BIN;1",
		"/LOWER.TXT;1",
		"/README.TXT;1",
		"/SUB/DEEPER/DEEP.TXT;1",
		"/SUB/DEEPER/EMPTY.TXT;1",
		"/SUB/NESTED.DAT;1",
	}
	return dir, want
}

func buildSample(t *testing.T, src string, opts *Options) string {
	t.Helper()
	b := New(opts)
	if err := b.AddTree("", src); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	n, err := b.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if n%LogicalSectorSize != 0 {
		t.Fatalf("image is %d bytes, not a whole number of %d-byte Logical Sectors", n, LogicalSectorSize)
	}
	out := filepath.Join(t.TempDir(), "test.iso")
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestIsoinfoAcceptsImage validates a written image with isoinfo, cdrkit's
// own ISO 9660 reader.
func TestIsoinfoAcceptsImage(t *testing.T) {
	if haveTool(t, "isoinfo") == "" {
		t.Skip("isoinfo not installed; cannot externally validate (install cdrkit/genisoimage)")
	}
	src, want := sampleTree(t)
	iso := buildSample(t, src, &Options{
		VolumeID:  "GOWIM_TEST",
		SystemID:  "GOWIM",
		Timestamp: time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
	})

	// -d prints the Primary Volume Descriptor, -f the full path list.
	out, err := exec.Command("isoinfo", "-d", "-i", iso).CombinedOutput()
	if err != nil {
		t.Fatalf("isoinfo -d failed: %v\n%s", err, out)
	}
	text := string(out)
	for _, want := range []string{
		"Volume id: GOWIM_TEST",
		"System id: GOWIM",
		"Logical block size is: 2048",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("isoinfo -d output missing %q; full output:\n%s", want, text)
		}
	}

	out, err = exec.Command("isoinfo", "-f", "-i", iso).CombinedOutput()
	if err != nil {
		t.Fatalf("isoinfo -f failed: %v\n%s", err, out)
	}
	got := parsePaths(string(out))
	if !equalStrings(got, want) {
		t.Errorf("isoinfo -f listed\n  %v\nwant\n  %v", got, want)
	}
}

// TestIsoinfoFileContents checks that the bytes isoinfo extracts for each
// file are the bytes that went in. This is the check that catches an extent
// or Data Length that is merely self-consistent but wrong.
func TestIsoinfoFileContents(t *testing.T) {
	if haveTool(t, "isoinfo") == "" {
		t.Skip("isoinfo not installed; cannot externally validate")
	}
	src, _ := sampleTree(t)
	iso := buildSample(t, src, &Options{VolumeID: "GOWIM_TEST"})

	cases := []struct{ isoPath, hostPath string }{
		{"/README.TXT;1", "README.TXT"},
		{"/HAS_DASH.BIN;1", "has-dash.bin"},
		{"/ANOTHER/SPANS_A_.BIN;1", "another/spans_a_sector.bin"},
		{"/SUB/DEEPER/DEEP.TXT;1", "sub/deeper/DEEP.TXT"},
		{"/SUB/NESTED.DAT;1", "sub/NESTED.DAT"},
	}
	for _, tc := range cases {
		want, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(tc.hostPath)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := exec.Command("isoinfo", "-i", iso, "-x", tc.isoPath).Output()
		if err != nil {
			t.Fatalf("isoinfo -x %s: %v", tc.isoPath, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: extracted %d bytes, want %d", tc.isoPath, len(got), len(want))
		}
	}
}

// TestXorrisoAcceptsImage validates the image with xorriso, an ISO 9660
// implementation with no shared code lineage with cdrkit.
func TestXorrisoAcceptsImage(t *testing.T) {
	if haveTool(t, "xorriso") == "" {
		t.Skip("xorriso not installed; cannot cross-validate against a second implementation")
	}
	src, want := sampleTree(t)
	iso := buildSample(t, src, &Options{VolumeID: "GOWIM_TEST"})

	out, err := exec.Command("xorriso", "-indev", iso, "-find", "/", "-exec", "echo", "--").CombinedOutput()
	if err != nil {
		t.Fatalf("xorriso failed: %v\n%s", err, out)
	}
	text := string(out)
	// xorriso reports structural problems as FAILURE/SORRY events; a clean
	// image produces none.
	for _, bad := range []string{"FAILURE", "SORRY"} {
		if strings.Contains(text, bad) {
			t.Errorf("xorriso reported %s:\n%s", bad, text)
		}
	}
	// xorriso strips the ";1" File Version Number when it prints a name,
	// so compare against the bare identifiers.
	for _, p := range want {
		p = strings.TrimSuffix(p, ";1")
		if !strings.Contains(text, "'"+p+"'") {
			t.Errorf("xorriso did not list %q; output:\n%s", p, text)
		}
	}
}

// TestMultiExtentLargeFile exercises the ECMA-119 6.5.1 multi-extent
// mechanism, the standard-conformant representation of a file too large for
// the 32-bit Data Length field of 9.1.4.
//
// A real >4 GiB file cannot be materialised in a unit test, so
// Options.MaxSectionSize is turned down to 2048 bytes, which makes a
// 10000-byte file take five File Sections and hence five Directory Records
// with the Multi-Extent flag set on the first four. The bytes an external
// reader reassembles must still equal the original file, which is precisely
// the property that matters at 4 GiB.
func TestMultiExtentLargeFile(t *testing.T) {
	if haveTool(t, "isoinfo") == "" {
		t.Skip("isoinfo not installed; cannot externally validate")
	}
	src := t.TempDir()
	content := bytes.Repeat([]byte("0123456789ABCDEF"), 625) // 10000 bytes
	if err := os.WriteFile(filepath.Join(src, "BIG.DAT"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	// Level3 is required: 10.1 and 10.2 both state that at Levels 1 and 2
	// each file shall consist of only one File Section.
	iso := buildSample(t, src, &Options{
		VolumeID:       "GOWIM_ME",
		Level:          Level3,
		MaxSectionSize: 2048,
	})

	got, err := exec.Command("isoinfo", "-i", iso, "-x", "/BIG.DAT;1").Output()
	if err != nil {
		t.Fatalf("isoinfo -x: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("reassembled %d bytes, want %d", len(got), len(content))
	}

	// And confirm the split really happened rather than the test silently
	// exercising the single-section path: isoinfo -l prints one line per
	// Directory Record, so the file must appear five times.
	out, err := exec.Command("isoinfo", "-i", iso, "-l").CombinedOutput()
	if err != nil {
		t.Fatalf("isoinfo -l: %v\n%s", err, out)
	}
	if n := strings.Count(string(out), "BIG.DAT"); n != 5 {
		t.Errorf("expected 5 Directory Records for the 5 File Sections, isoinfo -l showed %d:\n%s", n, out)
	}
}

// TestMultiExtentRejectedBelowLevel3 checks that a file needing more than
// one File Section is refused rather than silently truncated when the
// interchange level forbids it (ECMA-119 10.1, 10.2).
func TestMultiExtentRejectedBelowLevel3(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "BIG.DAT"), make([]byte, 10000), 0o644); err != nil {
		t.Fatal(err)
	}
	b := New(&Options{Level: Level2, MaxSectionSize: 2048})
	if err := b.AddTree("", src); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteTo(&bytes.Buffer{}); err == nil {
		t.Fatal("expected an error for a multi-section file at Level2")
	} else if !strings.Contains(err.Error(), "File Section") {
		t.Errorf("error did not explain the File Section restriction: %v", err)
	}
}

// TestCompareWithGenisoimage builds the same tree with genisoimage and with
// this package and reports the structural differences.
//
// The two images are not expected to be identical — genisoimage writes a
// version block, pads the end, and orders some things differently — so this
// test asserts only on the properties that must agree, and logs the rest so
// that a human reading the test output can see exactly where the two
// producers diverge.
func TestCompareWithGenisoimage(t *testing.T) {
	if haveTool(t, "genisoimage") == "" || haveTool(t, "isoinfo") == "" {
		t.Skip("genisoimage/isoinfo not installed; cannot compare against the reference producer")
	}
	src, _ := sampleTree(t)
	mine := buildSample(t, src, &Options{VolumeID: "GOWIM_TEST"})

	theirs := filepath.Join(t.TempDir(), "gen.iso")
	out, err := exec.Command("genisoimage", "-quiet", "-iso-level", "1",
		"-V", "GOWIM_TEST", "-o", theirs, src).CombinedOutput()
	if err != nil {
		t.Fatalf("genisoimage failed: %v\n%s", err, out)
	}

	minePaths := isoinfoPaths(t, mine)
	theirPaths := isoinfoPaths(t, theirs)
	if !equalStrings(minePaths, theirPaths) {
		t.Errorf("path lists differ.\n  gowim:       %v\n  genisoimage: %v", minePaths, theirPaths)
	}

	// Contents must match file for file.
	for _, p := range minePaths {
		a, err := exec.Command("isoinfo", "-i", mine, "-x", p).Output()
		if err != nil {
			t.Fatalf("isoinfo -x %s (gowim): %v", p, err)
		}
		b, err := exec.Command("isoinfo", "-i", theirs, "-x", p).Output()
		if err != nil {
			t.Fatalf("isoinfo -x %s (genisoimage): %v", p, err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("%s: contents differ (gowim %d bytes, genisoimage %d bytes)", p, len(a), len(b))
		}
	}

	// Log the size difference rather than asserting on it: genisoimage
	// emits an extra "version block" sector and, by default, 150 sectors
	// of run-out padding that this package omits (Options.PadSectors
	// defaults to 0), so its image is legitimately larger.
	sa, _ := os.Stat(mine)
	sb, _ := os.Stat(theirs)
	t.Logf("image sizes: gowim %d bytes (%d sectors), genisoimage %d bytes (%d sectors)",
		sa.Size(), sa.Size()/LogicalSectorSize, sb.Size(), sb.Size()/LogicalSectorSize)
}

func isoinfoPaths(t *testing.T, iso string) []string {
	t.Helper()
	out, err := exec.Command("isoinfo", "-f", "-i", iso).CombinedOutput()
	if err != nil {
		t.Fatalf("isoinfo -f %s: %v\n%s", iso, err, out)
	}
	return parsePaths(string(out))
}

// parsePaths turns isoinfo -f output into a sorted list of file paths.
// isoinfo -f prints one absolute path per line, directories included; the
// directories are dropped here so the comparison is over files only.
func parsePaths(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "/") {
			continue
		}
		out = append(out, line)
		seen[line] = true
	}
	// Drop any path that is a prefix of another, i.e. a directory.
	var files []string
	for _, p := range out {
		isDir := false
		for _, q := range out {
			if q != p && strings.HasPrefix(q, p+"/") {
				isDir = true
				break
			}
		}
		if !isDir {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	return files
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

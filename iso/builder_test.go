package iso

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNameCollisions checks that lossy mangling does not produce two
// Directory Records with the same File Identifier in one directory, and
// that the surviving records are still in the order ECMA-119 9.3 requires.
//
// Collisions are not an edge case: mangling folds case, substitutes every
// character that is not a d-character (7.4.1), and truncates to the Level1
// 8.3 limits (10.1), so "Foo.txt", "foo.txt" and "FOO.TXT" all reduce to
// the same identifier.
func TestNameCollisions(t *testing.T) {
	if haveTool(t, "isoinfo") == "" {
		t.Skip("isoinfo not installed; cannot externally validate")
	}
	src := t.TempDir()
	for _, n := range []string{
		"Foo.txt", "foo.txt", "FOO.TXT", "fo-o.txt",
		"verylongname1.dat", "verylongname2.dat", "verylongname3.dat",
	} {
		if err := os.WriteFile(filepath.Join(src, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	iso := buildSample(t, src, &Options{VolumeID: "COLLIDE"})

	got := isoinfoPaths(t, iso)
	if len(got) != 7 {
		t.Fatalf("expected 7 distinct identifiers, got %d: %v", len(got), got)
	}
	seen := map[string]bool{}
	for _, p := range got {
		if seen[p] {
			t.Errorf("duplicate identifier %q", p)
		}
		seen[p] = true
	}

	// isoinfo -f walks the directory records in the order they are
	// recorded, so its output is a direct read-out of the on-disc order.
	// The expected order is the 9.3 valuation with FILLER (0x20) padding,
	// which sorts a shorter name before a longer one sharing its prefix
	// and puts 'O' (0x4F) before '_' (0x5F).
	want := []string{
		"/FOO.TXT;1",
		"/FOO1.TXT;1",
		"/FOO2.TXT;1",
		"/FO_O.TXT;1",
		"/VERYLON1.DAT;1",
		"/VERYLON2.DAT;1",
		"/VERYLONG.DAT;1",
	}
	// isoinfoPaths sorts, so compare against the sorted expectation for
	// membership and check the recorded order separately.
	if !equalStrings(got, sortedCopy(want)) {
		t.Errorf("identifiers = %v, want %v", got, sortedCopy(want))
	}

	out, err := exec.Command("isoinfo", "-f", "-i", iso).Output()
	if err != nil {
		t.Fatal(err)
	}
	var recorded []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "/") {
			recorded = append(recorded, line)
		}
	}
	if !equalStrings(recorded, want) {
		t.Errorf("recorded order = %v,\nwant ECMA-119 9.3 order %v", recorded, want)
	}
}

func sortedCopy(s []string) []string {
	c := append([]string(nil), s...)
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j] < c[j-1]; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
	return c
}

// TestDeepHierarchyRejected checks the ECMA-119 6.8.2.1 limit is enforced
// rather than silently exceeded: "the number of levels in the hierarchy
// shall not exceed eight" for a hierarchy identified by a Primary Volume
// Descriptor.
//
// This matters for the eventual Windows-media goal, because a real
// extracted Windows ISO tree is ten levels deep. Producing an image that
// quietly violates the clause would push the failure onto whichever reader
// happens to enforce it; the error names what is missing instead.
func TestDeepHierarchyRejected(t *testing.T) {
	b := New(nil)
	if err := b.AddDir("a/b/c/d/e/f/g/h/i"); err != nil {
		t.Fatal(err)
	}
	_, err := b.WriteTo(&bytes.Buffer{})
	if err == nil {
		t.Fatal("expected an error for a 10-level hierarchy")
	}
	if !strings.Contains(err.Error(), "6.8.2.1") {
		t.Errorf("error should cite the clause it enforces: %v", err)
	}

	// Eight levels is exactly the limit and must be accepted.
	b = New(nil)
	if err := b.AddDir("a/b/c/d/e/f/g"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteTo(&bytes.Buffer{}); err != nil {
		t.Errorf("8 levels should be accepted: %v", err)
	}
}

// TestEmptyImage checks that a tree with no entries at all still produces a
// structurally valid volume: a root directory whose extent holds only the
// "." and ".." records that ECMA-119 6.8.2.2 requires, and a path table
// with the single root record of 6.9.
func TestEmptyImage(t *testing.T) {
	if haveTool(t, "isoinfo") == "" {
		t.Skip("isoinfo not installed; cannot externally validate")
	}
	iso := buildSample(t, t.TempDir(), &Options{VolumeID: "EMPTY"})
	out, err := exec.Command("isoinfo", "-d", "-i", iso).CombinedOutput()
	if err != nil {
		t.Fatalf("isoinfo rejected an empty image: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Volume id: EMPTY") {
		t.Errorf("isoinfo -d:\n%s", out)
	}
}

// TestMemSourceAndVirtualPaths exercises the Source abstraction with
// content that never touches the filesystem, which is how the deferred El
// Torito phase will record its generated boot catalog.
func TestMemSourceAndVirtualPaths(t *testing.T) {
	if haveTool(t, "isoinfo") == "" {
		t.Skip("isoinfo not installed; cannot externally validate")
	}
	b := New(&Options{VolumeID: "VIRTUAL"})
	payload := bytes.Repeat([]byte("generated\n"), 300)
	if err := b.AddFile("boot/BOOT.CAT", MemSource(payload)); err != nil {
		t.Fatal(err)
	}
	if err := b.AddFile("README.TXT", MemSource([]byte("virtual tree\n"))); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := b.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "virtual.iso")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := exec.Command("isoinfo", "-i", path, "-x", "/BOOT/BOOT.CAT;1").Output()
	if err != nil {
		t.Fatalf("isoinfo -x: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("extracted %d bytes, want %d", len(got), len(payload))
	}
}

// TestDirectorySpansSectors builds a directory with enough children that
// its extent needs several Logical Sectors, exercising the 6.8.1.1 rule
// that a Directory Record must end in the sector it begins in.
//
// This is the case an off-by-one in the packing logic would corrupt, and it
// would be invisible in a small tree: the records would simply run over a
// sector boundary and a reader would decode garbage from the middle of the
// directory onwards.
func TestDirectorySpansSectors(t *testing.T) {
	if haveTool(t, "isoinfo") == "" {
		t.Skip("isoinfo not installed; cannot externally validate")
	}
	src := t.TempDir()
	const n = 200
	for i := 0; i < n; i++ {
		name := "F" + pad4(i) + ".DAT"
		if err := os.WriteFile(filepath.Join(src, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	iso := buildSample(t, src, &Options{VolumeID: "MANYFILES"})
	got := isoinfoPaths(t, iso)
	if len(got) != n {
		t.Fatalf("expected %d files, isoinfo found %d", n, len(got))
	}
	// Read every one back: a record lost at a sector boundary would show
	// up either as a missing path above or as wrong content here.
	for _, p := range got {
		data, err := exec.Command("isoinfo", "-i", iso, "-x", p).Output()
		if err != nil {
			t.Fatalf("isoinfo -x %s: %v", p, err)
		}
		want := strings.TrimSuffix(strings.TrimPrefix(p, "/"), ";1")
		if string(data) != want {
			t.Errorf("%s: contents %q, want %q", p, data, want)
		}
	}
}

func pad4(i int) string {
	s := itoa(i)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

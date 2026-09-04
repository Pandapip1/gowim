package wim

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

// buildTestTree builds:
//
//	/ (root)
//	  Windows/
//	    System32/
//	      drivers/
//	        prnms001.inf   (unnamed stream hash {1,2,3})
//	  readme.txt           (unnamed stream hash {9,9,9})
func buildTestTree() *DirEntry {
	inf := &DirEntry{
		Name:    stringToUTF16LE("prnms001.inf"),
		Streams: []Stream{{Hash: Hash{1, 2, 3}}},
	}
	drivers := &DirEntry{
		Attributes: FileAttributeDirectory,
		SecurityID: SecurityIDNone,
		Name:       stringToUTF16LE("drivers"),
		Streams:    []Stream{{}},
		Children:   []*DirEntry{inf},
	}
	system32 := &DirEntry{
		Attributes: FileAttributeDirectory,
		SecurityID: SecurityIDNone,
		Name:       stringToUTF16LE("System32"),
		Streams:    []Stream{{}},
		Children:   []*DirEntry{drivers},
	}
	windows := &DirEntry{
		Attributes: FileAttributeDirectory,
		SecurityID: SecurityIDNone,
		Name:       stringToUTF16LE("Windows"),
		Streams:    []Stream{{}},
		Children:   []*DirEntry{system32},
	}
	readme := &DirEntry{
		Name:    stringToUTF16LE("readme.txt"),
		Streams: []Stream{{Hash: Hash{9, 9, 9}}},
	}
	root := &DirEntry{
		Attributes: FileAttributeDirectory,
		SecurityID: SecurityIDNone,
		Streams:    []Stream{{}},
		Children:   []*DirEntry{windows, readme},
	}
	return root
}

func TestLookupNestedBothSeparators(t *testing.T) {
	root := buildTestTree()

	for _, path := range []string{
		"Windows/System32/drivers/prnms001.inf",
		`Windows\System32\drivers\prnms001.inf`,
		`Windows/System32\drivers/prnms001.inf`,
		"/Windows/System32/drivers/prnms001.inf",
	} {
		got, err := root.Lookup(path)
		if err != nil {
			t.Fatalf("Lookup(%q) error = %v", path, err)
		}
		if got == nil || got.NameUTF8() != "prnms001.inf" {
			t.Fatalf("Lookup(%q) = %+v, want prnms001.inf", path, got)
		}
	}

	// Case-insensitive.
	got, err := root.Lookup("windows/system32/DRIVERS/PRNMS001.INF")
	if err != nil || got.NameUTF8() != "prnms001.inf" {
		t.Fatalf("case-insensitive Lookup failed: got=%v err=%v", got, err)
	}

	// Empty path resolves to root itself.
	got, err = root.Lookup("")
	if err != nil || got != root {
		t.Fatalf("Lookup(\"\") = %v, %v, want root, nil", got, err)
	}
}

func TestLookupMissingReturnsErrNotFound(t *testing.T) {
	root := buildTestTree()

	_, err := root.Lookup("Windows/System32/drivers/missing.sys")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrNotFound", err)
	}

	_, err = root.Lookup("Windows/nope/drivers")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrNotFound", err)
	}

	// Walking through a file as if it were a directory.
	_, err = root.Lookup("readme.txt/subpath")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrNotFound", err)
	}
}

func TestAddCreatesIntermediateDirectories(t *testing.T) {
	root := buildTestTree()

	hash := Hash{7, 7, 7}
	leaf, err := root.Add(`Windows\System32\drivers\store\newdrv\newdrv.inf`, hash)
	if err != nil {
		t.Fatalf("Add error = %v", err)
	}
	if leaf.NameUTF8() != "newdrv.inf" || leaf.MainHash() != hash {
		t.Fatalf("Add returned unexpected leaf: %+v", leaf)
	}

	got, err := root.Lookup("Windows/System32/drivers/store/newdrv/newdrv.inf")
	if err != nil {
		t.Fatalf("Lookup after Add error = %v", err)
	}
	if got.MainHash() != hash {
		t.Fatalf("Lookup after Add hash = %v, want %v", got.MainHash(), hash)
	}

	store, err := root.Lookup("Windows/System32/drivers/store")
	if err != nil || !store.IsDirectory() {
		t.Fatalf("intervening directory 'store' was not created as a directory: %+v, err=%v", store, err)
	}
}

func TestAddReplacesExistingFile(t *testing.T) {
	root := buildTestTree()
	newHash := Hash{0xaa}
	leaf, err := root.Add("Windows/System32/drivers/prnms001.inf", newHash)
	if err != nil {
		t.Fatalf("Add error = %v", err)
	}
	if leaf.MainHash() != newHash {
		t.Fatalf("Add did not replace hash: got %v want %v", leaf.MainHash(), newHash)
	}
	if len(leaf.Streams) != 1 {
		t.Fatalf("expected replacement to leave exactly one stream, got %d", len(leaf.Streams))
	}
}

func TestAddRejectsNonDirectoryComponent(t *testing.T) {
	root := buildTestTree()
	if _, err := root.Add("readme.txt/sub/file.txt", Hash{1}); err == nil {
		t.Fatal("expected error adding under a non-directory path component")
	}
}

func TestRemoveSingleFile(t *testing.T) {
	root := buildTestTree()
	if err := root.Remove("Windows/System32/drivers/prnms001.inf"); err != nil {
		t.Fatalf("Remove error = %v", err)
	}
	if _, err := root.Lookup("Windows/System32/drivers/prnms001.inf"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("file still resolves after Remove: err=%v", err)
	}
	// Sibling directory structure remains.
	if _, err := root.Lookup("Windows/System32/drivers"); err != nil {
		t.Fatalf("drivers directory unexpectedly removed: %v", err)
	}
}

func TestRemoveWholeSubtree(t *testing.T) {
	root := buildTestTree()
	if err := root.Remove("Windows/System32"); err != nil {
		t.Fatalf("Remove error = %v", err)
	}
	if _, err := root.Lookup("Windows/System32"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("System32 still resolves after Remove: err=%v", err)
	}
	if _, err := root.Lookup("Windows/System32/drivers/prnms001.inf"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("descendant of removed subtree still resolves: err=%v", err)
	}
	// Windows itself remains, just without System32.
	w, err := root.Lookup("Windows")
	if err != nil {
		t.Fatalf("Windows unexpectedly removed: %v", err)
	}
	if len(w.Children) != 0 {
		t.Fatalf("Windows still has children after removing its only child: %+v", w.Children)
	}
}

func TestRemoveMissingReturnsErrNotFound(t *testing.T) {
	root := buildTestTree()
	if err := root.Remove("Windows/nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrNotFound", err)
	}
}

func TestRenameMovesEntry(t *testing.T) {
	root := buildTestTree()
	if err := root.Rename("readme.txt", "docs/readme.md"); err != nil {
		t.Fatalf("Rename error = %v", err)
	}
	if _, err := root.Lookup("readme.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old path still resolves after Rename: err=%v", err)
	}
	got, err := root.Lookup("docs/readme.md")
	if err != nil {
		t.Fatalf("Lookup new path after Rename error = %v", err)
	}
	if got.MainHash() != (Hash{9, 9, 9}) {
		t.Fatalf("renamed entry lost its stream hash: %+v", got)
	}
}

func TestRenameRejectsExistingDestination(t *testing.T) {
	root := buildTestTree()
	if err := root.Rename("readme.txt", "Windows/System32/drivers/prnms001.inf"); err == nil {
		t.Fatal("expected error renaming onto an existing destination")
	}
}

func TestRenameRejectsMoveIntoOwnSubtree(t *testing.T) {
	root := buildTestTree()
	if err := root.Rename("Windows", "Windows/System32/Windows"); err == nil {
		t.Fatal("expected error moving a directory into its own subtree")
	}
}

func TestReadDirListsChildren(t *testing.T) {
	root := buildTestTree()
	children, err := root.ReadDir("Windows/System32")
	if err != nil {
		t.Fatalf("ReadDir error = %v", err)
	}
	if len(children) != 1 || children[0].NameUTF8() != "drivers" {
		t.Fatalf("ReadDir = %+v, want [drivers]", children)
	}
}

func TestReadDirRejectsNonDirectory(t *testing.T) {
	root := buildTestTree()
	if _, err := root.ReadDir("readme.txt"); err == nil {
		t.Fatal("expected error reading a non-directory as a directory")
	}
}

func TestMatchName(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"prn*", "prnms001.inf", true},
		{"prn*", "PRNMS001.INF", true},
		{"PRN*", "prnms001.inf", true},
		{"*.inf", "prnms001.inf", true},
		{"*.INF", "prnms001.inf", true},
		{"mfd*", "mfdrv.sys", true},
		{"mfd*", "prnms001.inf", false},
		{"prnms001.inf", "prnms001.inf", true},
		{"prnms001.inf", "prnms002.inf", false},
		{"readme.txt", "README.TXT", true},
		{"prn??001.inf", "prnms001.inf", true},
		{"prn?001.inf", "prnms001.inf", false},
	}
	for _, tc := range cases {
		if got := MatchName(tc.pattern, tc.name); got != tc.want {
			t.Errorf("MatchName(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestReadFileUncompressed(t *testing.T) {
	content := []byte("hello, wim")
	hash := Hash{0x42}

	rh := ResourceHeader{
		SizeInWIM:        uint64(len(content)),
		OffsetInWIM:      0,
		UncompressedSize: uint64(len(content)),
	}
	bt := &BlobTable{Entries: []BlobDescriptor{{Resource: rh, Hash: hash}}}

	root := &DirEntry{
		Attributes: FileAttributeDirectory,
		SecurityID: SecurityIDNone,
		Streams:    []Stream{{}},
		Children: []*DirEntry{
			{Name: stringToUTF16LE("a.txt"), Streams: []Stream{{Hash: hash}}},
		},
	}

	r := &Reader{ra: bytes.NewReader(content), size: int64(len(content))}
	got, err := r.ReadFile(root, bt, "a.txt")
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("ReadFile = %q, want %q", got, content)
	}
}

// TestReadFileCompressedReturnsErrCompressedResourceUnmodified covers solid
// resources specifically: this package now transparently decompresses
// non-solid compressed resources (see DecodeResourceData), so
// ErrCompressedResource is reserved for the one remaining unsupported case,
// ResFlagSolid (see BlobTable.SolidResourceRun).
func TestReadFileCompressedReturnsErrCompressedResourceUnmodified(t *testing.T) {
	hash := Hash{0x99}
	rh := ResourceHeader{
		SizeInWIM:        10,
		Flags:            ResFlagCompressed | ResFlagSolid,
		OffsetInWIM:      0,
		UncompressedSize: 100,
	}
	bt := &BlobTable{Entries: []BlobDescriptor{{Resource: rh, Hash: hash}}}

	root := &DirEntry{
		Attributes: FileAttributeDirectory,
		SecurityID: SecurityIDNone,
		Streams:    []Stream{{}},
		Children: []*DirEntry{
			{Name: stringToUTF16LE("big.bin"), Streams: []Stream{{Hash: hash}}},
		},
	}

	r := &Reader{ra: bytes.NewReader(make([]byte, 10)), size: 10}
	_, err := r.ReadFile(root, bt, "big.bin")
	if !errors.Is(err, ErrCompressedResource) {
		t.Fatalf("error = %v, want ErrCompressedResource", err)
	}
	if err != ErrCompressedResource {
		t.Fatalf("error was wrapped/modified: %v, want the sentinel unmodified", err)
	}
}

// TestChildEmptyDirectory covers the zero-children case, which the
// name->child index must handle without a nil-map panic.
func TestChildEmptyDirectory(t *testing.T) {
	empty := &DirEntry{Attributes: FileAttributeDirectory, SecurityID: SecurityIDNone}
	if got := empty.Child("anything"); got != nil {
		t.Fatalf("Child on empty directory = %v, want nil", got)
	}
}

// TestChildCaseInsensitive covers the exact scenario described in the
// package's Child doc comment: looking up a name that differs only in case
// from a real child's name must still find it, via the cached index.
func TestChildCaseInsensitive(t *testing.T) {
	root := buildTestTree()
	windows, err := root.Lookup("Windows/System32/drivers")
	if err != nil {
		t.Fatalf("Lookup(drivers) error = %v", err)
	}
	for _, name := range []string{"prnms001.inf", "PRNMS001.INF", "PrNmS001.iNf"} {
		got := windows.Child(name)
		if got == nil || got.NameUTF8() != "prnms001.inf" {
			t.Fatalf("Child(%q) = %v, want prnms001.inf", name, got)
		}
	}
}

// TestChildIndexInvalidatedByMutation exercises the index cache's
// invalidation path directly: it forces the index to be built (a first
// Child call), mutates Children (add, then remove), and confirms every
// subsequent Child call reflects the current state rather than a stale
// cached map.
func TestChildIndexInvalidatedByMutation(t *testing.T) {
	dir := &DirEntry{Attributes: FileAttributeDirectory, SecurityID: SecurityIDNone}

	// Build the index against an empty directory.
	if got := dir.Child("a.txt"); got != nil {
		t.Fatalf("Child(a.txt) on empty dir = %v, want nil", got)
	}

	a := &DirEntry{Name: stringToUTF16LE("a.txt"), Streams: []Stream{{Hash: Hash{1}}}}
	dir.Children = append(dir.Children, a)
	if got := dir.Child("A.TXT"); got != a {
		t.Fatalf("Child(A.TXT) after add = %v, want %v", got, a)
	}

	b := &DirEntry{Name: stringToUTF16LE("b.txt"), Streams: []Stream{{Hash: Hash{2}}}}
	dir.Children = append(dir.Children, b)
	if got := dir.Child("b.txt"); got != b {
		t.Fatalf("Child(b.txt) after second add = %v, want %v", got, b)
	}
	// The first child must still resolve correctly after the index was
	// rebuilt for the second add.
	if got := dir.Child("a.txt"); got != a {
		t.Fatalf("Child(a.txt) after second add = %v, want %v", got, a)
	}

	// Remove a.txt (mirroring how Remove itself reassigns Children) and
	// confirm the index no longer finds it, while b.txt still resolves.
	dir.Children = append(dir.Children[:0:0], b)
	if got := dir.Child("a.txt"); got != nil {
		t.Fatalf("Child(a.txt) after removal = %v, want nil", got)
	}
	if got := dir.Child("b.txt"); got != b {
		t.Fatalf("Child(b.txt) after removal of a.txt = %v, want %v", got, b)
	}
}

// TestChildIndexRemoveViaPackageAPI covers the same invalidation concern but
// through the package's own mutating APIs (Add/Remove/Rename), rather than
// direct field surgery, so a regression in how those APIs reassign Children
// would also be caught here.
func TestChildIndexRemoveViaPackageAPI(t *testing.T) {
	root := buildTestTree()

	// Warm the index at the drivers directory, then add a sibling via Add.
	drivers, err := root.Lookup("Windows/System32/drivers")
	if err != nil {
		t.Fatalf("Lookup error = %v", err)
	}
	if drivers.Child("newdrv.inf") != nil {
		t.Fatal("newdrv.inf unexpectedly present before Add")
	}
	if _, err := root.Add(`Windows\System32\drivers\newdrv.inf`, Hash{5}); err != nil {
		t.Fatalf("Add error = %v", err)
	}
	if drivers.Child("NEWDRV.INF") == nil {
		t.Fatal("newdrv.inf not found via cached DirEntry after Add")
	}

	if err := root.Remove("Windows/System32/drivers/prnms001.inf"); err != nil {
		t.Fatalf("Remove error = %v", err)
	}
	if drivers.Child("prnms001.inf") != nil {
		t.Fatal("prnms001.inf still resolves via cached DirEntry after Remove")
	}
	if drivers.Child("newdrv.inf") == nil {
		t.Fatal("newdrv.inf missing via cached DirEntry after sibling Remove")
	}

	if err := root.Rename("Windows/System32/drivers/newdrv.inf", "Windows/System32/drivers/renamed.inf"); err != nil {
		t.Fatalf("Rename error = %v", err)
	}
	if drivers.Child("newdrv.inf") != nil {
		t.Fatal("newdrv.inf still resolves via cached DirEntry after Rename")
	}
	if drivers.Child("RENAMED.INF") == nil {
		t.Fatal("renamed.inf not found via cached DirEntry after Rename")
	}
}

// TestChildLargeDirectory mirrors the real-scale scenario the performance
// audit measured (a directory with tens of thousands of children, like a
// real image's Windows\WinSxS\Manifests) and exercises the index path at
// that scale: every synthetic child must still resolve by exact name and by
// a case-varied name, and a name that was never added must not.
func TestChildLargeDirectory(t *testing.T) {
	const n = 28069
	dir := &DirEntry{Attributes: FileAttributeDirectory, SecurityID: SecurityIDNone}
	names := make([]string, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("amd64_component.name.%05d_31bf3856ad364e35_10.0.22621.6120_none_%08x.manifest", i, i)
		names[i] = name
		dir.Children = append(dir.Children, &DirEntry{
			Name:    stringToUTF16LE(name),
			Streams: []Stream{{Hash: Hash{byte(i), byte(i >> 8)}}},
		})
	}

	// Spot-check across the range (first, middle, last) both exact-case and
	// upper-cased, plus a miss.
	for _, i := range []int{0, 1, n / 2, n - 2, n - 1} {
		got := dir.Child(names[i])
		if got == nil || got.NameUTF8() != names[i] {
			t.Fatalf("Child(%q) = %v, want the entry at index %d", names[i], got, i)
		}
		upper := fmt.Sprintf("AMD64_COMPONENT.NAME.%05d_31BF3856AD364E35_10.0.22621.6120_NONE_%08X.MANIFEST", i, i)
		got = dir.Child(upper)
		if got == nil || got.NameUTF8() != names[i] {
			t.Fatalf("Child(%q) (case-varied) = %v, want the entry at index %d", upper, got, i)
		}
	}
	if got := dir.Child("does-not-exist.manifest"); got != nil {
		t.Fatalf("Child(missing) = %v, want nil", got)
	}
}

func TestReadFileMissingReturnsErrNotFound(t *testing.T) {
	root := buildTestTree()
	bt := &BlobTable{}
	r := &Reader{ra: bytes.NewReader(nil), size: 0}
	_, err := r.ReadFile(root, bt, "Windows/nope.txt")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrNotFound", err)
	}
}

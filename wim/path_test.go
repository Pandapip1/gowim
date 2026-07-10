package wim

import (
	"bytes"
	"errors"
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

func TestReadFileMissingReturnsErrNotFound(t *testing.T) {
	root := buildTestTree()
	bt := &BlobTable{}
	r := &Reader{ra: bytes.NewReader(nil), size: 0}
	_, err := r.ReadFile(root, bt, "Windows/nope.txt")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrNotFound", err)
	}
}

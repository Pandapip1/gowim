package wim

import (
	"crypto/sha1"
	"testing"
)

// buildDonorTree returns a small tree: root/keep/a.txt (SecurityID 0),
// root/keep/sub/b.txt (SecurityID 1, HardLinkGroupID set, real timestamps),
// root/drop/c.txt (SecurityID 0, same descriptor as a.txt -- exercises
// dedup) -- and its SecurityData. "drop" is never grafted, only "keep";
// tests confirm its descriptor (though shared with a.txt) still ends up
// referenced because a.txt uses it too.
func buildDonorTree(t *testing.T) (*DirEntry, *SecurityData) {
	t.Helper()

	sd := &SecurityData{Descriptors: [][]byte{
		[]byte("descriptor-0"),
		[]byte("descriptor-1"),
	}}

	aHash := Hash(sha1.Sum([]byte("a")))
	bHash := Hash(sha1.Sum([]byte("b")))
	cHash := Hash(sha1.Sum([]byte("c")))

	a := &DirEntry{
		Name:       stringToUTF16LE("a.txt"),
		SecurityID: 0,
		Streams:    []Stream{{Hash: aHash}},
	}
	b := &DirEntry{
		Name:            stringToUTF16LE("b.txt"),
		SecurityID:      1,
		HardLinkGroupID: 99,
		CreationTime:    111,
		LastWriteTime:   222,
		ShortName:       stringToUTF16LE("B~1.TXT"),
		Streams:         []Stream{{Hash: bHash}},
	}
	sub := &DirEntry{
		Name:       stringToUTF16LE("sub"),
		Attributes: FileAttributeDirectory,
		SecurityID: SecurityIDNone,
		Children:   []*DirEntry{b},
	}
	keep := &DirEntry{
		Name:       stringToUTF16LE("keep"),
		Attributes: FileAttributeDirectory,
		SecurityID: SecurityIDNone,
		Children:   []*DirEntry{a, sub},
	}
	c := &DirEntry{
		Name:       stringToUTF16LE("c.txt"),
		SecurityID: 0,
		Streams:    []Stream{{Hash: cHash}},
	}
	drop := &DirEntry{
		Name:       stringToUTF16LE("drop"),
		Attributes: FileAttributeDirectory,
		SecurityID: SecurityIDNone,
		Children:   []*DirEntry{c},
	}
	root := &DirEntry{
		Attributes: FileAttributeDirectory,
		SecurityID: SecurityIDNone,
		Children:   []*DirEntry{keep, drop},
	}
	return root, sd
}

func TestSecurityRemapperCopyPreservesFieldsAndRemaps(t *testing.T) {
	donorRoot, _ := buildDonorTree(t)
	keep, err := donorRoot.Lookup("keep")
	if err != nil {
		t.Fatalf("Lookup(keep): %v", err)
	}

	r := NewSecurityRemapper()
	copied := r.Copy(keep)

	a := copied.Child("a.txt")
	if a == nil {
		t.Fatalf("copied tree missing a.txt")
	}
	if a.SecurityID != 0 {
		t.Fatalf("a.txt remapped SecurityID = %d, want 0 (first-seen)", a.SecurityID)
	}
	if a.MainHash() != Hash(sha1.Sum([]byte("a"))) {
		t.Fatalf("a.txt hash not preserved")
	}

	sub := copied.Child("sub")
	if sub == nil {
		t.Fatalf("copied tree missing sub")
	}
	b := sub.Child("b.txt")
	if b == nil {
		t.Fatalf("copied tree missing sub/b.txt")
	}
	if b.SecurityID != 1 {
		t.Fatalf("b.txt remapped SecurityID = %d, want 1 (second-seen)", b.SecurityID)
	}
	if b.HardLinkGroupID != 99 || b.CreationTime != 111 || b.LastWriteTime != 222 {
		t.Fatalf("b.txt real fields not preserved: %+v", b)
	}
	if b.NameUTF8() != "b.txt" {
		t.Fatalf("b.txt name = %q", b.NameUTF8())
	}
	if got := utf16leToString(b.ShortName); got != "B~1.TXT" {
		t.Fatalf("b.txt ShortName = %q, want B~1.TXT", got)
	}

	// copied must be an independent tree: mutating it must not affect the
	// donor's original entries.
	a.HardLinkGroupID = 12345
	origA, _ := keep.Lookup("a.txt")
	if origA.HardLinkGroupID == 12345 {
		t.Fatalf("Copy aliased the donor's own DirEntry instead of copying it")
	}
}

func TestSecurityRemapperBuildSecurityData(t *testing.T) {
	donorRoot, sd := buildDonorTree(t)
	keep, _ := donorRoot.Lookup("keep")

	r := NewSecurityRemapper()
	r.Copy(keep)
	newSD := r.BuildSecurityData(sd)

	if len(newSD.Descriptors) != 2 {
		t.Fatalf("BuildSecurityData: got %d descriptors, want 2 (a.txt's + b.txt's)", len(newSD.Descriptors))
	}
	if string(newSD.Descriptors[0]) != "descriptor-0" {
		t.Fatalf("descriptor 0 = %q, want descriptor-0 (a.txt's, first-seen)", newSD.Descriptors[0])
	}
	if string(newSD.Descriptors[1]) != "descriptor-1" {
		t.Fatalf("descriptor 1 = %q, want descriptor-1 (b.txt's, second-seen)", newSD.Descriptors[1])
	}
}

func TestAttachAtCreatesIntermediateDirsAndPreservesNode(t *testing.T) {
	donorRoot, _ := buildDonorTree(t)
	keep, _ := donorRoot.Lookup("keep")

	r := NewSecurityRemapper()
	copied := r.Copy(keep)

	newRoot := &DirEntry{Attributes: FileAttributeDirectory, SecurityID: SecurityIDNone}
	if err := AttachAt(newRoot, `Windows\Boot`, copied); err != nil {
		t.Fatalf("AttachAt: %v", err)
	}

	windows := newRoot.Child("Windows")
	if windows == nil || !windows.IsDirectory() {
		t.Fatalf("AttachAt did not create intermediate Windows directory")
	}
	boot := windows.Child("Boot")
	if boot == nil {
		t.Fatalf("AttachAt did not attach node as Boot")
	}
	if boot.NameUTF8() != "Boot" {
		t.Fatalf("attached node Name = %q, want Boot (set from path, not original Name)", boot.NameUTF8())
	}
	if boot.Child("a.txt") == nil {
		t.Fatalf("attached node lost its own children")
	}
}

func TestAttachAtRejectsDuplicate(t *testing.T) {
	root := &DirEntry{Attributes: FileAttributeDirectory, SecurityID: SecurityIDNone}
	if err := AttachAt(root, "foo", &DirEntry{}); err != nil {
		t.Fatalf("first AttachAt: %v", err)
	}
	if err := AttachAt(root, "foo", &DirEntry{}); err == nil {
		t.Fatalf("second AttachAt at same path: want error, got nil")
	}
}

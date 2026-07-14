package component

import (
	"crypto/sha1"
	"errors"
	"testing"

	"github.com/Pandapip1/gowim/wim"
)

func newTestRoot() *wim.DirEntry {
	return &wim.DirEntry{Attributes: wim.FileAttributeDirectory, SecurityID: wim.SecurityIDNone}
}

func addTestFile(t *testing.T, root *wim.DirEntry, bt *wim.BlobTable, path string, content string) {
	t.Helper()
	hash := wim.Hash(sha1.Sum([]byte(content)))
	if _, err := root.Add(path, hash); err != nil {
		t.Fatalf("Add(%s): %v", path, err)
	}
	bt.Entries = append(bt.Entries, wim.BlobDescriptor{Hash: hash, RefCount: 1})
}

func TestRemoveKindPackage(t *testing.T) {
	root := newTestRoot()
	bt := &wim.BlobTable{}
	const name = "Contoso-Package~31bf3856ad364e35~amd64~~10.0.22621.1.mum"
	const base = "Contoso-Package~31bf3856ad364e35~amd64~~10.0.22621.1"

	addTestFile(t, root, bt, PackagesDir+`\`+name, "mum contents")
	addTestFile(t, root, bt, PackagesDir+`\`+base+".cat", "cat contents")

	e := &Entry{Kind: KindPackage, FileName: name}
	if err := Remove(root, bt, e); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := root.Lookup(PackagesDir + `\` + name); !errors.Is(err, wim.ErrNotFound) {
		t.Errorf(".mum still present, err = %v", err)
	}
	if _, err := root.Lookup(PackagesDir + `\` + base + ".cat"); !errors.Is(err, wim.ErrNotFound) {
		t.Errorf(".cat still present, err = %v", err)
	}
	for _, d := range bt.Entries {
		if d.RefCount != 0 {
			t.Errorf("blob %v RefCount = %d, want 0", d.Hash, d.RefCount)
		}
	}
}

func TestRemoveKindComponentWithPayloadDir(t *testing.T) {
	root := newTestRoot()
	bt := &wim.BlobTable{}
	const name = "amd64_contoso.driver_31bf3856ad364e35_10.0.22621.1_none_deadbeefcafebabe.manifest"
	const base = "amd64_contoso.driver_31bf3856ad364e35_10.0.22621.1_none_deadbeefcafebabe"

	addTestFile(t, root, bt, ManifestsDir+`\`+name, "manifest contents")
	addTestFile(t, root, bt, WinSxSDir+`\`+base+`\contoso.sys`, "payload contents")

	e := &Entry{Kind: KindComponent, FileName: name}
	if err := Remove(root, bt, e); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := root.Lookup(ManifestsDir + `\` + name); !errors.Is(err, wim.ErrNotFound) {
		t.Errorf(".manifest still present, err = %v", err)
	}
	if _, err := root.Lookup(WinSxSDir + `\` + base); !errors.Is(err, wim.ErrNotFound) {
		t.Errorf("payload directory still present, err = %v", err)
	}
	for _, d := range bt.Entries {
		if d.RefCount != 0 {
			t.Errorf("blob %v RefCount = %d, want 0", d.Hash, d.RefCount)
		}
	}
}

// TestRemoveKindComponentNoPayloadDir reproduces the real, common case
// (4216 of 17189 real .manifest files in a real Windows 11 23H2 image, see
// Remove's doc comment) of a policy/metadata-only component with no WinSxS
// payload directory at all: Remove must still succeed, deleting only the
// .manifest entry.
func TestRemoveKindComponentNoPayloadDir(t *testing.T) {
	root := newTestRoot()
	bt := &wim.BlobTable{}
	const name = "amd64_policy.contoso_31bf3856ad364e35_10.0.22621.1_none_deadbeefcafebabe.manifest"

	addTestFile(t, root, bt, ManifestsDir+`\`+name, "manifest contents")

	e := &Entry{Kind: KindComponent, FileName: name}
	if err := Remove(root, bt, e); err != nil {
		t.Fatalf("Remove of a policy-only component (no payload dir) returned an error: %v", err)
	}

	if _, err := root.Lookup(ManifestsDir + `\` + name); !errors.Is(err, wim.ErrNotFound) {
		t.Errorf(".manifest still present, err = %v", err)
	}
}

func TestRemoveAlreadyGoneIsSuccess(t *testing.T) {
	root := newTestRoot()

	pkg := &Entry{Kind: KindPackage, FileName: "Nonexistent-Package~31bf3856ad364e35~amd64~~1.0.0.0.mum"}
	if err := Remove(root, nil, pkg); err != nil {
		t.Errorf("Remove of an already-absent package entry returned an error: %v", err)
	}

	comp := &Entry{Kind: KindComponent, FileName: "amd64_nonexistent_31bf3856ad364e35_1.0.0.0_none_deadbeefcafebabe.manifest"}
	if err := Remove(root, nil, comp); err != nil {
		t.Errorf("Remove of an already-absent component entry returned an error: %v", err)
	}
}

func TestRemoveUnknownKind(t *testing.T) {
	root := newTestRoot()
	e := &Entry{Kind: Kind(99), FileName: "whatever"}
	if err := Remove(root, nil, e); err == nil {
		t.Error("expected an error for an unknown Kind")
	}
}

func TestRemoveNilArgs(t *testing.T) {
	if err := Remove(nil, nil, &Entry{}); err == nil {
		t.Error("expected an error for a nil root")
	}
	if err := Remove(newTestRoot(), nil, nil); err == nil {
		t.Error("expected an error for a nil entry")
	}
}

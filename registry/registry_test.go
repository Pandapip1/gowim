package registry

import (
	"bytes"
	"crypto/sha1"
	"testing"

	"github.com/Pandapip1/gowim/regf"
	"github.com/Pandapip1/gowim/wim"
)

// buildTestHiveBytes builds a small but structurally valid SYSTEM-shaped
// hive (a root key with one REG_SZ marker value), matching the sibling
// regf package's own TestBuildFromStructLiterals fixture shape.
func buildTestHiveBytes(t *testing.T, marker string) []byte {
	t.Helper()
	hive := &regf.Hive{
		BaseBlock: regf.BaseBlock{
			MajorVersion:     1,
			MinorVersion:     regf.Version1_5,
			FileType:         regf.FileTypePrimary,
			ClusteringFactor: 1,
		},
		Root: &regf.Key{
			Flags:  regf.KeyFlagHiveEntry,
			Values: []regf.Value{{Name: regf.EncodeSZ("Marker"), Type: regf.RegSZ, Data: regf.EncodeSZ(marker)}},
		},
	}
	data, err := hive.AppendTo(nil)
	if err != nil {
		t.Fatalf("buildTestHiveBytes: AppendTo: %v", err)
	}
	return data
}

// buildTestImage places files (a map of image-relative path -> content)
// into a real, serialized-and-reparsed WIM image, returning a Reader/root
// DirEntry/BlobTable trio exactly as a real caller reading a real WIM file
// would get from wim.NewReader/(*Reader).ImageMetadata/(*Reader).BlobTable
// -- mirroring the sibling wim package's own writer_test.go fixture
// approach, so this package's tests exercise the real
// Reader.ReadFile/DirEntry.Lookup code paths, not a hand-rolled stand-in.
func buildTestImage(t *testing.T, files map[string][]byte) (*wim.Reader, *wim.DirEntry, *wim.BlobTable) {
	t.Helper()

	bt := &wim.BlobTable{}
	src := wim.MapBlobSource{}
	root := &wim.DirEntry{
		Attributes: wim.FileAttributeDirectory,
		SecurityID: wim.SecurityIDNone,
	}
	for path, data := range files {
		hash := wim.Hash(sha1.Sum(data))
		bt.Entries = append(bt.Entries, wim.BlobDescriptor{Hash: hash, PartNumber: 1, RefCount: 1})
		src[hash] = data
		if _, err := root.Add(path, hash); err != nil {
			t.Fatalf("buildTestImage: Add(%s): %v", path, err)
		}
	}
	images := []*wim.ImageMetadata{{Security: &wim.SecurityData{}, Root: root}}

	wimBytes, err := wim.Assemble(images, bt, &wim.XMLData{}, src, wim.WriteOptions{GUID: wim.GUID{1}})
	if err != nil {
		t.Fatalf("buildTestImage: Assemble: %v", err)
	}

	r, err := wim.NewReader(bytes.NewReader(wimBytes), int64(len(wimBytes)))
	if err != nil {
		t.Fatalf("buildTestImage: NewReader: %v", err)
	}
	rbt, err := r.BlobTable()
	if err != nil {
		t.Fatalf("buildTestImage: BlobTable: %v", err)
	}
	metas := rbt.MetadataResources()
	if len(metas) != 1 {
		t.Fatalf("buildTestImage: got %d image metadata resources, want 1", len(metas))
	}
	im, err := r.ImageMetadata(metas[0])
	if err != nil {
		t.Fatalf("buildTestImage: ImageMetadata: %v", err)
	}
	return r, im.Root, rbt
}

func TestLoadHiveSetPartial(t *testing.T) {
	r, root, bt := buildTestImage(t, map[string][]byte{
		`Windows\System32\config\SYSTEM`:   buildTestHiveBytes(t, "system-v1"),
		`Windows\System32\config\SOFTWARE`: buildTestHiveBytes(t, "software-v1"),
	})

	hs, err := LoadHiveSet(r, root, bt)
	if err != nil {
		t.Fatalf("LoadHiveSet: %v", err)
	}
	if len(hs.Hives) != 2 {
		t.Fatalf("len(Hives) = %d, want 2 (SYSTEM+SOFTWARE only; DEFAULT/SAM/COMPONENTS/NTUSER.DAT absent from this image)", len(hs.Hives))
	}
	sys, ok := hs.Hives[HiveSystem]
	if !ok {
		t.Fatal("HiveSystem missing from HiveSet")
	}
	if sys.Path != `Windows\System32\config\SYSTEM` {
		t.Errorf("SYSTEM hive Path = %q", sys.Path)
	}
	if v := sys.Hive.Root.Value("Marker"); v == nil || v.SZ() != "system-v1" {
		t.Errorf("SYSTEM hive Root Marker value = %+v, want %q", v, "system-v1")
	}
	if _, ok := hs.Hives[HiveComponents]; ok {
		t.Error("HiveComponents present, want absent (not in this test image)")
	}
}

func TestLoadHiveSetEmpty(t *testing.T) {
	r, root, bt := buildTestImage(t, map[string][]byte{
		`Windows\System32\SomeOtherFile.txt`: []byte("not a hive"),
	})

	hs, err := LoadHiveSet(r, root, bt)
	if err != nil {
		t.Fatalf("LoadHiveSet: %v", err)
	}
	if len(hs.Hives) != 0 {
		t.Errorf("len(Hives) = %d, want 0 (no standard hives present)", len(hs.Hives))
	}
}

func TestHiveSaveUpdatesEntryAndBlobTable(t *testing.T) {
	origData := buildTestHiveBytes(t, "before")
	r, root, bt := buildTestImage(t, map[string][]byte{
		`Windows\System32\config\SYSTEM`: origData,
	})
	origHash := wim.Hash(sha1.Sum(origData))

	hs, err := LoadHiveSet(r, root, bt)
	if err != nil {
		t.Fatalf("LoadHiveSet: %v", err)
	}
	sys := hs.Hives[HiveSystem]

	// Mutate the in-memory tree via the sibling regf package's generic Key
	// API, then save it back.
	sys.Hive.Root.SetValue("Marker", regf.RegSZ, regf.EncodeSZ("after"))

	blob, err := sys.Save(bt)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if blob.Data == nil {
		t.Fatal("Save returned a zero NewBlob, want genuinely new content")
	}
	newHash := wim.Hash(sha1.Sum(blob.Data))
	if blob.Hash != newHash {
		t.Errorf("NewBlob.Hash = %x, want %x", blob.Hash, newHash)
	}

	// Entry.Streams must now point at the new hash.
	if got := sys.Entry.MainHash(); got != newHash {
		t.Errorf("Entry.MainHash() = %x, want %x (new content)", got, newHash)
	}

	// The blob table must have a live entry for the new hash...
	newDesc, ok := findBlobDescriptor(bt, newHash)
	if !ok {
		t.Fatal("no blob-table entry for the new hash")
	}
	if newDesc.RefCount != 1 {
		t.Errorf("new hash RefCount = %d, want 1", newDesc.RefCount)
	}
	// ...and the old hash's RefCount must have been decremented to 0 (this
	// was its only reference), not removed outright (see Save's doc
	// comment: reclaiming zero-RefCount entries is a whole-WIM-aware
	// concern for a higher-level caller).
	oldDesc, ok := findBlobDescriptor(bt, origHash)
	if !ok {
		t.Fatal("old hash's blob-table entry was removed outright, want RefCount 0 but still present")
	}
	if oldDesc.RefCount != 0 {
		t.Errorf("old hash RefCount = %d, want 0", oldDesc.RefCount)
	}

	// Re-parsing the new bytes must reflect the mutation.
	reparsed, err := regf.Parse(blob.Data)
	if err != nil {
		t.Fatalf("regf.Parse(new bytes): %v", err)
	}
	if v := reparsed.Root.Value("Marker"); v == nil || v.SZ() != "after" {
		t.Errorf("reparsed Marker value = %+v, want %q", v, "after")
	}
}

func TestHiveSaveUnchangedIsNotANewBlob(t *testing.T) {
	origData := buildTestHiveBytes(t, "unchanged")
	r, root, bt := buildTestImage(t, map[string][]byte{
		`Windows\System32\config\SYSTEM`: origData,
	})

	hs, err := LoadHiveSet(r, root, bt)
	if err != nil {
		t.Fatalf("LoadHiveSet: %v", err)
	}
	sys := hs.Hives[HiveSystem]

	// Save without modifying anything: AppendTo of an unmodified,
	// freshly-Parsed tree reproduces the same bytes (see regf's own
	// TestParseAppendToRoundTrip), so this should be a no-op against the
	// blob table -- no new blob, RefCount unchanged.
	blob, err := sys.Save(bt)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if blob.Data != nil {
		t.Errorf("Save on unmodified hive returned new blob data (%d bytes), want none (same hash as before)", len(blob.Data))
	}

	origHash := wim.Hash(sha1.Sum(origData))
	desc, ok := findBlobDescriptor(bt, origHash)
	if !ok {
		t.Fatal("original hash's blob-table entry is gone")
	}
	if desc.RefCount != 1 {
		t.Errorf("RefCount = %d, want 1 (unchanged: no decrement-then-recount should have happened)", desc.RefCount)
	}
}

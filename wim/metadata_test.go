package wim

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"reflect"
	"testing"
)

// --- Reference ("old") implementation, kept only in this test file --------
//
// This is a faithful copy of ImageMetadata.AppendTo and
// appendDirEntryTreeBased exactly as they were before the copy/allocation
// optimization (see wim/metadata.go and wim/dentry_write.go): two throwaway
// intermediate buffers built via unhinted append, plus a second
// map[*DirEntry]uint64 built just to bias offsets by 'base'. It is used
// solely as an independent oracle to confirm the optimized implementation
// produces byte-identical output, and to benchmark the "before" behavior
// against the "after" behavior in the same test binary/run.

func oldAssignSubdirOffsets(root *DirEntry) (map[*DirEntry]uint64, uint64) {
	offsets := make(map[*DirEntry]uint64)
	cursor := root.outLen() + 8

	walk := func(dir *DirEntry) {
		if !dir.IsDirectory() {
			offsets[dir] = 0
			return
		}
		offsets[dir] = cursor
		for _, c := range dir.Children {
			cursor += c.outLen()
		}
		cursor += 8
	}
	var order []*DirEntry
	var collect func(d *DirEntry)
	collect = func(d *DirEntry) {
		order = append(order, d)
		for _, c := range d.Children {
			collect(c)
		}
	}
	collect(root)
	for _, d := range order {
		walk(d)
	}
	return offsets, cursor
}

func oldAppendDirEntryTreeBased(dst []byte, root *DirEntry, base uint64) ([]byte, error) {
	if root == nil {
		return dst, fmt.Errorf("wim: cannot serialize a nil dentry tree root")
	}
	offsets, _ := oldAssignSubdirOffsets(root)
	biased := make(map[*DirEntry]uint64, len(offsets))
	for d, off := range offsets {
		if off != 0 {
			biased[d] = off + base
		} else {
			biased[d] = 0
		}
	}

	var err error
	if dst, err = root.appendDentry(dst, biased[root]); err != nil {
		return dst, err
	}
	dst = append(dst, make([]byte, 8)...)

	var walk func(dir *DirEntry) error
	walk = func(dir *DirEntry) error {
		if dir.IsDirectory() && biased[dir] != 0 {
			for _, c := range dir.Children {
				if dst, err = c.appendDentry(dst, biased[c]); err != nil {
					return err
				}
			}
			dst = append(dst, make([]byte, 8)...)
		}
		for _, c := range dir.Children {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return dst, err
	}
	return dst, nil
}

func oldImageMetadataAppendTo(m *ImageMetadata, dst []byte) ([]byte, error) {
	if m.Security == nil {
		m.Security = &SecurityData{}
	}
	if m.Root == nil {
		return dst, fmt.Errorf("wim: image metadata has no root directory entry")
	}

	sdBytes := m.Security.AppendTo(nil)
	base := uint64(len(sdBytes))

	treeBytes, err := oldAppendDirEntryTreeBased(nil, m.Root, base)
	if err != nil {
		return dst, err
	}

	dst = append(dst, sdBytes...)
	dst = append(dst, treeBytes...)
	return dst, nil
}

// --- Test fixtures ----------------------------------------------------------

// buildSyntheticTree constructs a moderately-sized (or, with larger
// numDirs/filesPerDir, a large) DirEntry tree with numDirs subdirectories of
// the root, each containing filesPerDir files, for use in tests and
// benchmarks that need a realistic dentry-tree shape.
func buildSyntheticTree(t testing.TB, numDirs, filesPerDir int) *DirEntry {
	t.Helper()
	root := &DirEntry{
		Attributes: FileAttributeDirectory,
		SecurityID: SecurityIDNone,
	}
	for i := 0; i < numDirs; i++ {
		for j := 0; j < filesPerDir; j++ {
			path := fmt.Sprintf("dir%05d/file%05d.dat", i, j)
			h := sha1.Sum([]byte(path))
			if _, err := root.Add(path, Hash(h)); err != nil {
				t.Fatalf("Add(%s): %v", path, err)
			}
		}
	}
	return root
}

// buildSyntheticSecurity constructs a non-trivial SecurityData table so that
// 'base' in AppendTo is nonzero and multi-descriptor, exercising the
// bias-by-base logic under test.
func buildSyntheticSecurity(n int) *SecurityData {
	sd := &SecurityData{}
	for i := 0; i < n; i++ {
		desc := bytes.Repeat([]byte{byte(i)}, 68+(i%5)*4)
		sd.Descriptors = append(sd.Descriptors, desc)
	}
	return sd
}

// TestImageMetadataAppendToByteIdentical confirms that the optimized
// ImageMetadata.AppendTo (single exact-capacity allocation, offsets biased at
// the source) produces byte-for-byte identical output to the original
// two-intermediate-buffer, second-map implementation, for a tree with nested
// directories and a non-empty (multi-descriptor) security table.
func TestImageMetadataAppendToByteIdentical(t *testing.T) {
	root := buildSyntheticTree(t, 8, 12)
	sd := buildSyntheticSecurity(5)

	got, err := (&ImageMetadata{Security: sd, Root: root}).AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}

	want, err := oldImageMetadataAppendTo(&ImageMetadata{Security: sd, Root: root}, nil)
	if err != nil {
		t.Fatalf("oldImageMetadataAppendTo: %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("AppendTo output differs from reference implementation: got %d bytes, want %d bytes", len(got), len(want))
	}
}

// TestImageMetadataAppendToWithPrefix confirms AppendTo still appends
// correctly (and identically to the reference implementation) when dst
// already has content, i.e. the exact-capacity growth path is exercised with
// a nonzero starting length.
func TestImageMetadataAppendToWithPrefix(t *testing.T) {
	root := buildSyntheticTree(t, 4, 6)
	sd := buildSyntheticSecurity(3)
	prefix := []byte("some-preexisting-header-bytes")

	got, err := (&ImageMetadata{Security: sd, Root: root}).AppendTo(append([]byte(nil), prefix...))
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}

	want, err := oldImageMetadataAppendTo(&ImageMetadata{Security: sd, Root: root}, append([]byte(nil), prefix...))
	if err != nil {
		t.Fatalf("oldImageMetadataAppendTo: %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("AppendTo output (with prefix) differs from reference implementation")
	}
	if !bytes.HasPrefix(got, prefix) {
		t.Fatalf("AppendTo did not preserve the existing dst prefix")
	}
}

// TestImageMetadataAppendToRoundTrip confirms the serialized metadata resource
// decodes back into a structurally identical tree, as an end-to-end sanity
// check on top of the byte-identical comparisons above.
func TestImageMetadataAppendToRoundTrip(t *testing.T) {
	root := buildSyntheticTree(t, 6, 9)
	sd := buildSyntheticSecurity(4)

	buf, err := (&ImageMetadata{Security: sd, Root: root}).AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}

	parsed, err := ParseImageMetadata(buf)
	if err != nil {
		t.Fatalf("ParseImageMetadata: %v", err)
	}

	reEncoded, err := parsed.AppendTo(nil)
	if err != nil {
		t.Fatalf("re-AppendTo: %v", err)
	}
	if !bytes.Equal(buf, reEncoded) {
		t.Fatalf("round-tripped metadata did not re-encode identically")
	}
	if !reflect.DeepEqual(sd.Descriptors, parsed.Security.Descriptors) {
		t.Fatalf("security descriptors did not round-trip")
	}
}

// --- Benchmarks --------------------------------------------------------------
//
// These benchmark the full ImageMetadata.AppendTo assembly path (the
// optimized version currently in metadata.go, and the "old" reference kept
// above) over a synthetic tree with tens of thousands of dentries, to measure
// the effect of the copy/allocation changes described in metadata.go's
// AppendTo doc comment. Run with:
//
//	go test ./wim/ -run NONE -bench ImageMetadataAppendTo -benchmem

func benchmarkTree(b *testing.B) (*DirEntry, *SecurityData) {
	b.Helper()
	// 200 dirs x 150 files/dir = ~30,200 dentries (dirs + files).
	root := buildSyntheticTree(b, 200, 150)
	sd := buildSyntheticSecurity(20)
	return root, sd
}

func BenchmarkImageMetadataAppendTo_New(b *testing.B) {
	root, sd := benchmarkTree(b)
	m := &ImageMetadata{Security: sd, Root: root}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := m.AppendTo(nil); err != nil {
			b.Fatalf("AppendTo: %v", err)
		}
	}
}

func BenchmarkImageMetadataAppendTo_Old(b *testing.B) {
	root, sd := benchmarkTree(b)
	m := &ImageMetadata{Security: sd, Root: root}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := oldImageMetadataAppendTo(m, nil); err != nil {
			b.Fatalf("oldImageMetadataAppendTo: %v", err)
		}
	}
}

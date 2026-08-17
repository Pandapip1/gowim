package wim

import (
	"bytes"
	"crypto/sha1"
	"strings"
	"testing"
)

// threeImageFixture returns three images: image1 and image2 share one blob
// ("shared12.bin"); image3 shares nothing with the others. Each image has a
// distinctive <DESCRIPTION> so we can spot-check XML survival after export.
func threeImageFixture(t *testing.T) ([]*ImageMetadata, *BlobTable, MapBlobSource, *XMLData) {
	t.Helper()

	bt := &BlobTable{}
	src := MapBlobSource{}
	seen := make(map[Hash]int)

	addBlob := func(data []byte) Hash {
		hash := Hash(sha1.Sum(data))
		if idx, ok := seen[hash]; ok {
			bt.Entries[idx].RefCount++
		} else {
			bt.Entries = append(bt.Entries, BlobDescriptor{Hash: hash, PartNumber: 1, RefCount: 1})
			seen[hash] = len(bt.Entries) - 1
			src[hash] = data
		}
		return hash
	}

	shared12 := bytes.Repeat([]byte("shared between image 1 and 2 "), 100)

	filesPerImage := []map[string][]byte{
		{"a.txt": []byte("image one content"), "shared12.bin": shared12},
		{"b.txt": []byte("image two content"), "shared12.bin": shared12},
		{"c.txt": []byte("image three content")},
	}

	var images []*ImageMetadata
	for _, files := range filesPerImage {
		root := &DirEntry{
			Attributes: FileAttributeDirectory,
			SecurityID: SecurityIDNone,
			Streams:    []Stream{{}},
		}
		for name, data := range files {
			hash := addBlob(data)
			if _, err := root.Add(name, hash); err != nil {
				t.Fatalf("Add(%s): %v", name, err)
			}
		}
		images = append(images, &ImageMetadata{Security: &SecurityData{}, Root: root})
	}

	xml := &XMLData{Document: `<WIM>` +
		`<IMAGE INDEX="1"><NAME>one</NAME><DESCRIPTION>desc-one</DESCRIPTION><WINDOWS><EDITIONID>EditionOne</EDITIONID></WINDOWS></IMAGE>` +
		`<IMAGE INDEX="2"><NAME>two</NAME><DESCRIPTION>desc-two</DESCRIPTION></IMAGE>` +
		`<IMAGE INDEX="3"><NAME>three</NAME><DESCRIPTION>desc-three</DESCRIPTION></IMAGE>` +
		`</WIM>`}

	return images, bt, src, xml
}

func assembleSource(t *testing.T, images []*ImageMetadata, bt *BlobTable, xmlData *XMLData, src MapBlobSource) *Reader {
	t.Helper()
	wimBytes, err := Assemble(images, bt, xmlData, src, WriteOptions{
		CompressionType: HdrFlagCompressXPRESS,
		ChunkSize:       32768,
		GUID:            GUID{1},
	})
	if err != nil {
		t.Fatalf("Assemble source WIM: %v", err)
	}
	r, err := NewReader(bytes.NewReader(wimBytes), int64(len(wimBytes)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return r
}

// TestRebuildBlobTableReclaimsUnreferencedBlobs reproduces the exact real-world
// gap RebuildBlobTable exists to close: driver.Uninstall/appx.Remove/
// component.Remove-style callers only ever decrement a BlobDescriptor's
// RefCount when a file is deleted from the tree, never remove the entry
// itself. Confirms RebuildBlobTable drops a genuinely zero-referenced blob
// entirely (not just zeroes its count) while correctly recomputing a
// still-shared blob's count after one of its two references is removed.
func TestRebuildBlobTableReclaimsUnreferencedBlobs(t *testing.T) {
	images, bt, _, _ := threeImageFixture(t)

	// shared12.bin is referenced by both image1 and image2 (RefCount 2 in the
	// fixture). Remove it from image2 only, mimicking a real removal helper:
	// decrement RefCount by hand (as driver.Uninstall/appx.Remove/
	// component.Remove all do) without touching the BlobTable entry itself.
	if err := images[1].Root.Remove("shared12.bin"); err != nil {
		t.Fatalf("Remove(shared12.bin) from image2: %v", err)
	}
	for i := range bt.Entries {
		hash := Hash(sha1.Sum(bytes.Repeat([]byte("shared between image 1 and 2 "), 100)))
		if bt.Entries[i].Hash == hash {
			bt.Entries[i].RefCount--
		}
	}

	// image3's c.txt blob is removed entirely from its only image, simulating
	// a blob that should be fully reclaimed (dropped from the rebuilt table,
	// not merely left at RefCount 0 for WriteTo to still faithfully persist).
	if err := images[2].Root.Remove("c.txt"); err != nil {
		t.Fatalf("Remove(c.txt) from image3: %v", err)
	}
	cHash := Hash(sha1.Sum([]byte("image three content")))
	for i := range bt.Entries {
		if bt.Entries[i].Hash == cHash {
			bt.Entries[i].RefCount--
		}
	}

	// Sanity: the pre-rebuild table still carries the stale, now-orphaned
	// c.txt entry at RefCount 0 -- exactly the bug WriteTo would otherwise
	// silently still write to disk.
	if d, ok := bt.ByHash(cHash); !ok || d.RefCount != 0 {
		t.Fatalf("fixture setup: expected c.txt blob present at RefCount 0 before rebuild, got %+v, ok=%v", d, ok)
	}

	rebuilt, err := RebuildBlobTable(images, bt)
	if err != nil {
		t.Fatalf("RebuildBlobTable: %v", err)
	}

	if _, ok := rebuilt.ByHash(cHash); ok {
		t.Fatalf("RebuildBlobTable: orphaned c.txt blob %s should have been dropped entirely, still present", cHash)
	}

	sharedHash := Hash(sha1.Sum(bytes.Repeat([]byte("shared between image 1 and 2 "), 100)))
	sharedDesc, ok := rebuilt.ByHash(sharedHash)
	if !ok {
		t.Fatalf("RebuildBlobTable: shared12.bin blob should still be present (still referenced by image1)")
	}
	if sharedDesc.RefCount != 1 {
		t.Fatalf("RebuildBlobTable: shared12.bin RefCount = %d, want 1 (only image1 still references it)", sharedDesc.RefCount)
	}

	// a.txt (image1) and b.txt (image2) are untouched and must survive with
	// RefCount 1 each.
	for _, content := range []string{"image one content", "image two content"} {
		hash := Hash(sha1.Sum([]byte(content)))
		desc, ok := rebuilt.ByHash(hash)
		if !ok {
			t.Fatalf("RebuildBlobTable: blob for %q should still be present", content)
		}
		if desc.RefCount != 1 {
			t.Fatalf("RebuildBlobTable: blob for %q RefCount = %d, want 1", content, desc.RefCount)
		}
	}

	if got, want := len(rebuilt.Entries), 3; got != want {
		t.Fatalf("RebuildBlobTable: got %d entries, want %d (a.txt, b.txt, shared12.bin)", got, want)
	}
}

// TestRebuildBlobTableMissingBlobErrors confirms RebuildBlobTable fails loudly
// (rather than silently producing an unwriteable BlobTable) if a tree
// references a hash genuinely absent from src, mirroring ExportImage's own
// existing validation.
func TestRebuildBlobTableMissingBlobErrors(t *testing.T) {
	images, bt, _, _ := threeImageFixture(t)
	empty := &BlobTable{}
	if _, err := RebuildBlobTable(images, empty); err == nil {
		t.Fatalf("RebuildBlobTable: expected error for blobs missing from src, got nil")
	}
	_ = bt
}

func TestExportImageSingle(t *testing.T) {
	images, bt, src, xmlData := threeImageFixture(t)
	r := assembleSource(t, images, bt, xmlData, src)

	srcBT, err := r.BlobTable()
	if err != nil {
		t.Fatalf("BlobTable: %v", err)
	}
	srcXML, err := r.XMLData()
	if err != nil {
		t.Fatalf("XMLData: %v", err)
	}

	// Export only image 2 (which references the shared blob, but image 1 --
	// the other referencer -- is not exported).
	out, err := ExportImageAssemble(r, srcBT, srcXML, []int{2}, WriteOptions{
		CompressionType: HdrFlagCompressXPRESS,
		ChunkSize:       32768,
		GUID:            GUID{1},
	})
	if err != nil {
		t.Fatalf("ExportImageAssemble: %v", err)
	}

	er, err := NewReader(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatalf("NewReader(exported): %v", err)
	}
	if er.Header().ImageCount != 1 {
		t.Fatalf("ImageCount = %d, want 1", er.Header().ImageCount)
	}

	ebt, err := er.BlobTable()
	if err != nil {
		t.Fatalf("BlobTable(exported): %v", err)
	}
	meta := ebt.MetadataResources()
	if len(meta) != 1 {
		t.Fatalf("MetadataResources: got %d, want 1", len(meta))
	}
	im, err := er.ImageMetadata(meta[0])
	if err != nil {
		t.Fatalf("ImageMetadata: %v", err)
	}

	// File contents round-trip.
	got, err := er.ReadFile(im.Root, ebt, "b.txt")
	if err != nil {
		t.Fatalf("ReadFile(b.txt): %v", err)
	}
	if string(got) != "image two content" {
		t.Fatalf("b.txt content = %q", got)
	}
	got, err = er.ReadFile(im.Root, ebt, "shared12.bin")
	if err != nil {
		t.Fatalf("ReadFile(shared12.bin): %v", err)
	}
	wantShared := bytes.Repeat([]byte("shared between image 1 and 2 "), 100)
	if !bytes.Equal(got, wantShared) {
		t.Fatalf("shared12.bin content mismatch")
	}

	// RefCount for the shared blob must be 1 in the export (only image 2 is
	// exported, even though the shared blob had RefCount 2 in the source).
	sharedHash := Hash(sha1.Sum(wantShared))
	desc, ok := ebt.ByHash(sharedHash)
	if !ok {
		t.Fatalf("shared blob missing from exported blob table")
	}
	if desc.RefCount != 1 {
		t.Fatalf("exported shared blob RefCount = %d, want 1", desc.RefCount)
	}

	// image1-only blob ("a.txt") must not appear at all.
	aHash := Hash(sha1.Sum([]byte("image one content")))
	if _, ok := ebt.ByHash(aHash); ok {
		t.Fatalf("exported blob table unexpectedly contains image1-only blob")
	}

	// XML per-image content survived, renumbered to index 1.
	exml, err := er.XMLData()
	if err != nil {
		t.Fatalf("XMLData(exported): %v", err)
	}
	if !strings.Contains(exml.Document, `INDEX="1"`) {
		t.Fatalf("exported XML missing INDEX=\"1\": %s", exml.Document)
	}
	if !strings.Contains(exml.Document, "desc-two") {
		t.Fatalf("exported XML missing desc-two: %s", exml.Document)
	}
	if strings.Contains(exml.Document, "desc-one") || strings.Contains(exml.Document, "desc-three") {
		t.Fatalf("exported XML leaked non-exported image content: %s", exml.Document)
	}
	if strings.Contains(exml.Document, `INDEX="2"`) {
		t.Fatalf("exported XML still has old index 2: %s", exml.Document)
	}
}

func TestExportImageMultiReorder(t *testing.T) {
	images, bt, src, xmlData := threeImageFixture(t)
	r := assembleSource(t, images, bt, xmlData, src)

	srcBT, err := r.BlobTable()
	if err != nil {
		t.Fatalf("BlobTable: %v", err)
	}
	srcXML, err := r.XMLData()
	if err != nil {
		t.Fatalf("XMLData: %v", err)
	}

	// Export images 3 and 1, in that (reordered) order -- and since both 1
	// and (not-exported) 2 reference shared12.bin, but only 1 is exported
	// here, RefCount should be 1.
	out, err := ExportImageAssemble(r, srcBT, srcXML, []int{3, 1}, WriteOptions{
		CompressionType: HdrFlagCompressLZX,
		ChunkSize:       32768,
		GUID:            GUID{1},
	})
	if err != nil {
		t.Fatalf("ExportImageAssemble: %v", err)
	}

	er, err := NewReader(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatalf("NewReader(exported): %v", err)
	}
	if er.Header().ImageCount != 2 {
		t.Fatalf("ImageCount = %d, want 2", er.Header().ImageCount)
	}
	ebt, err := er.BlobTable()
	if err != nil {
		t.Fatalf("BlobTable(exported): %v", err)
	}
	meta := ebt.MetadataResources()
	if len(meta) != 2 {
		t.Fatalf("MetadataResources: got %d, want 2", len(meta))
	}

	// Exported index 1 must be original image 3 ("c.txt"), index 2 must be
	// original image 1 ("a.txt", "shared12.bin").
	im1, err := er.ImageMetadata(meta[0])
	if err != nil {
		t.Fatalf("ImageMetadata(0): %v", err)
	}
	if _, err := er.ReadFile(im1.Root, ebt, "c.txt"); err != nil {
		t.Fatalf("exported index 1 should be original image 3 (c.txt): %v", err)
	}
	im2, err := er.ImageMetadata(meta[1])
	if err != nil {
		t.Fatalf("ImageMetadata(1): %v", err)
	}
	got, err := er.ReadFile(im2.Root, ebt, "a.txt")
	if err != nil {
		t.Fatalf("exported index 2 should be original image 1 (a.txt): %v", err)
	}
	if string(got) != "image one content" {
		t.Fatalf("a.txt content = %q", got)
	}

	wantShared := bytes.Repeat([]byte("shared between image 1 and 2 "), 100)
	sharedHash := Hash(sha1.Sum(wantShared))
	desc, ok := ebt.ByHash(sharedHash)
	if !ok {
		t.Fatalf("shared blob missing from exported blob table")
	}
	if desc.RefCount != 1 {
		t.Fatalf("exported shared blob RefCount = %d, want 1 (only image 1 of the two referencers exported)", desc.RefCount)
	}

	exml, err := er.XMLData()
	if err != nil {
		t.Fatalf("XMLData(exported): %v", err)
	}
	if !strings.Contains(exml.Document, "desc-three") || !strings.Contains(exml.Document, "desc-one") {
		t.Fatalf("exported XML missing expected descriptions: %s", exml.Document)
	}
	if strings.Contains(exml.Document, "desc-two") {
		t.Fatalf("exported XML leaked non-exported image 2 content: %s", exml.Document)
	}
	// Original image 1 had a <WINDOWS><EDITIONID> we should have preserved
	// verbatim via the innerxml technique (not just the light-parsed
	// XMLImage fields, which don't even round-trip this element).
	if !strings.Contains(exml.Document, "EditionOne") {
		t.Fatalf("exported XML lost <WINDOWS><EDITIONID> detail: %s", exml.Document)
	}
}

func TestExportImageAllCompressed(t *testing.T) {
	images, bt, src, xmlData := threeImageFixture(t)
	r := assembleSource(t, images, bt, xmlData, src)
	srcBT, _ := r.BlobTable()
	srcXML, _ := r.XMLData()

	// Recompress with a different type than the source (source used XPRESS).
	out, err := ExportImageAssemble(r, srcBT, srcXML, []int{1}, WriteOptions{
		CompressionType: HdrFlagCompressLZMS,
		ChunkSize:       131072,
		GUID:            GUID{1},
	})
	if err != nil {
		t.Fatalf("ExportImageAssemble: %v", err)
	}
	er, err := NewReader(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	ctype, err := er.Header().CompressionType()
	if err != nil {
		t.Fatalf("CompressionType: %v", err)
	}
	if ctype != HdrFlagCompressLZMS {
		t.Fatalf("CompressionType = %#x, want LZMS", ctype)
	}
	ebt, _ := er.BlobTable()
	meta := ebt.MetadataResources()
	im, err := er.ImageMetadata(meta[0])
	if err != nil {
		t.Fatalf("ImageMetadata: %v", err)
	}
	got, err := er.ReadFile(im.Root, ebt, "a.txt")
	if err != nil {
		t.Fatalf("ReadFile(a.txt): %v", err)
	}
	if string(got) != "image one content" {
		t.Fatalf("a.txt content = %q", got)
	}
}

func TestExportImageValidation(t *testing.T) {
	images, bt, src, xmlData := threeImageFixture(t)
	r := assembleSource(t, images, bt, xmlData, src)
	srcBT, _ := r.BlobTable()
	srcXML, _ := r.XMLData()

	if _, err := ExportImageAssemble(r, srcBT, srcXML, nil, WriteOptions{}); err == nil {
		t.Fatalf("ExportImageAssemble with no indices: want error, got nil")
	}
	if _, err := ExportImageAssemble(r, srcBT, srcXML, []int{99}, WriteOptions{}); err == nil {
		t.Fatalf("ExportImageAssemble with out-of-range index: want error, got nil")
	}
	if _, err := ExportImageAssemble(r, srcBT, srcXML, []int{0}, WriteOptions{}); err == nil {
		t.Fatalf("ExportImageAssemble with index 0: want error, got nil")
	}
	if _, err := ExportImageAssemble(nil, srcBT, srcXML, []int{1}, WriteOptions{}); err == nil {
		t.Fatalf("ExportImageAssemble with nil reader: want error, got nil")
	}
	if _, err := ExportImageAssemble(r, nil, srcXML, []int{1}, WriteOptions{}); err == nil {
		t.Fatalf("ExportImageAssemble with nil blob table: want error, got nil")
	}
}

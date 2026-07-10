package wim

import (
	"bytes"
	"crypto/sha1"
	"math/rand"
	"testing"
)

// buildTestImages constructs two images' worth of ImageMetadata/DirEntry
// trees sharing one blob by hash (to exercise dedup/RefCount), plus the
// (already-deduplicated) BlobTable and a MapBlobSource with every referenced
// blob's content. filesPerImage maps image index (0 or 1) to a map of
// path -> content for that image.
func buildTestImages(t *testing.T, filesPerImage [2]map[string][]byte) ([]*ImageMetadata, *BlobTable, MapBlobSource) {
	t.Helper()

	bt := &BlobTable{}
	src := MapBlobSource{}
	seen := make(map[Hash]int) // hash -> index into bt.Entries

	var images []*ImageMetadata
	for _, files := range filesPerImage {
		root := &DirEntry{
			Attributes: FileAttributeDirectory,
			SecurityID: SecurityIDNone,
			Streams:    []Stream{{}},
		}
		for name, data := range files {
			hash := Hash(sha1.Sum(data))
			if idx, ok := seen[hash]; ok {
				bt.Entries[idx].RefCount++
			} else {
				bt.Entries = append(bt.Entries, BlobDescriptor{Hash: hash, PartNumber: 1, RefCount: 1})
				seen[hash] = len(bt.Entries) - 1
				src[hash] = data
			}
			if _, err := root.Add(name, hash); err != nil {
				t.Fatalf("Add(%s): %v", name, err)
			}
		}
		images = append(images, &ImageMetadata{Security: &SecurityData{}, Root: root})
	}
	return images, bt, src
}

// verifyWIM reads back wimBytes with this package's own Reader and confirms
// every file in every image round-trips, ImageCount and MetadataResources
// counts are right, and RefCounts of shared blobs reflect the sharing.
func verifyWIM(t *testing.T, wimBytes []byte, filesPerImage [2]map[string][]byte, wantImageCount uint32) {
	t.Helper()

	r, err := NewReader(bytes.NewReader(wimBytes), int64(len(wimBytes)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	hdr := r.Header()
	if hdr.ImageCount != wantImageCount {
		t.Fatalf("Header.ImageCount = %d, want %d", hdr.ImageCount, wantImageCount)
	}

	bt, err := r.BlobTable()
	if err != nil {
		t.Fatalf("BlobTable: %v", err)
	}
	meta := bt.MetadataResources()
	if len(meta) != int(wantImageCount) {
		t.Fatalf("MetadataResources: got %d, want %d", len(meta), wantImageCount)
	}

	for i := 0; i < int(wantImageCount); i++ {
		im, err := r.ImageMetadata(meta[i])
		if err != nil {
			t.Fatalf("ImageMetadata(%d): %v", i, err)
		}
		for name, want := range filesPerImage[i] {
			got, err := r.ReadFile(im.Root, bt, name)
			if err != nil {
				t.Fatalf("image %d: ReadFile(%s): %v", i, name, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("image %d: ReadFile(%s): mismatch (got %d bytes, want %d bytes)", i, name, len(got), len(want))
			}
		}
	}

	// Confirm the shared blob (if any) has RefCount reflecting both images.
	for name0, data0 := range filesPerImage[0] {
		for name1, data1 := range filesPerImage[1] {
			if bytes.Equal(data0, data1) {
				h := Hash(sha1.Sum(data0))
				desc, ok := bt.ByHash(h)
				if !ok {
					t.Fatalf("shared blob (image0:%s, image1:%s) not found in blob table", name0, name1)
				}
				if desc.RefCount < 2 {
					t.Fatalf("shared blob RefCount = %d, want >= 2", desc.RefCount)
				}
			}
		}
	}
}

// twoImageFixture returns a deterministic two-image file set: distinct
// content per image plus one blob ("shared.bin") shared between both, and
// one file ("multichunk.bin") large enough to span multiple chunks at a
// 32768-byte chunk size.
func twoImageFixture() [2]map[string][]byte {
	rnd := rand.New(rand.NewSource(42))
	multi := make([]byte, 200000)
	rnd.Read(multi)
	shared := bytes.Repeat([]byte("shared blob content "), 500)

	return [2]map[string][]byte{
		{
			"tiny.bin":       {0x01},
			"text1.txt":      bytes.Repeat([]byte("image one "), 2000),
			"multichunk.bin": multi,
			"shared.bin":     shared,
		},
		{
			"text2.txt":  bytes.Repeat([]byte("image two "), 3000),
			"empty.bin":  {},
			"shared.bin": shared,
		},
	}
}

func TestWriteToMultiImageAllCompressionTypes(t *testing.T) {
	cases := []struct {
		name      string
		ctype     CompressionType
		chunkSize uint32
	}{
		{"uncompressed", CompressionNone, 0},
		{"xpress", HdrFlagCompressXPRESS, 32768},
		{"lzx", HdrFlagCompressLZX, 32768},
		{"lzms", HdrFlagCompressLZMS, 131072},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := twoImageFixture()
			images, bt, src := buildTestImages(t, files)

			xml := &XMLData{Document: `<WIM><IMAGE INDEX="1"><NAME>image one</NAME></IMAGE><IMAGE INDEX="2"><NAME>image two</NAME></IMAGE></WIM>`}

			wimBytes, err := Assemble(images, bt, xml, src, WriteOptions{
				CompressionType: tc.ctype,
				ChunkSize:       tc.chunkSize,
				BootIndex:       1,
			})
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}

			verifyWIM(t, wimBytes, files, 2)

			r, err := NewReader(bytes.NewReader(wimBytes), int64(len(wimBytes)))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			hdr := r.Header()
			if hdr.BootIndex != 1 {
				t.Fatalf("BootIndex = %d, want 1", hdr.BootIndex)
			}
			if hdr.BootMetadata.IsZero() {
				t.Fatalf("BootMetadata is zero, want set")
			}
			wantCType := tc.ctype
			gotCType, err := hdr.CompressionType()
			if err != nil {
				t.Fatalf("CompressionType: %v", err)
			}
			if tc.ctype == CompressionNone {
				if gotCType != CompressionNone {
					t.Fatalf("CompressionType = %#x, want CompressionNone", gotCType)
				}
			} else if gotCType != wantCType {
				t.Fatalf("CompressionType = %#x, want %#x", gotCType, wantCType)
			}

			// Blob table and XML data must be stored uncompressed, matching
			// real WIMs (see writer.go's doc comment).
			if hdr.BlobTable.IsCompressed() {
				t.Fatalf("blob table resource is compressed, want uncompressed")
			}
			if hdr.XMLData.IsCompressed() {
				t.Fatalf("xml data resource is compressed, want uncompressed")
			}
			if !hdr.BlobTable.IsMetadata() {
				t.Fatalf("blob table resource missing ResFlagMetadata")
			}
			if !hdr.XMLData.IsMetadata() {
				t.Fatalf("xml data resource missing ResFlagMetadata")
			}

			// Every metadata resource must be flagged as such, and (for
			// compressed WIMs) actually compressed, matching real WIMs.
			for i, m := range mustBlobTable(t, r).MetadataResources() {
				if !m.IsMetadata() {
					t.Fatalf("metadata resource %d missing ResFlagMetadata", i)
				}
				if m.IsSolid() {
					t.Fatalf("metadata resource %d unexpectedly solid", i)
				}
				if tc.ctype != CompressionNone && !m.IsCompressed() {
					t.Fatalf("metadata resource %d not compressed, want compressed", i)
				}
				if tc.ctype == CompressionNone && m.IsCompressed() {
					t.Fatalf("metadata resource %d compressed, want uncompressed", i)
				}
			}

			wantVersion := VersionDefault
			if tc.ctype == HdrFlagCompressLZMS {
				wantVersion = VersionSolid
			}
			if hdr.Version != wantVersion {
				t.Fatalf("Version = %#x, want %#x", hdr.Version, wantVersion)
			}
		})
	}
}

func mustBlobTable(t *testing.T, r *Reader) *BlobTable {
	t.Helper()
	bt, err := r.BlobTable()
	if err != nil {
		t.Fatalf("BlobTable: %v", err)
	}
	return bt
}

func TestWriteToNoBootImage(t *testing.T) {
	files := twoImageFixture()
	images, bt, src := buildTestImages(t, files)
	xml := &XMLData{Document: `<WIM><IMAGE INDEX="1"><NAME>a</NAME></IMAGE><IMAGE INDEX="2"><NAME>b</NAME></IMAGE></WIM>`}

	wimBytes, err := Assemble(images, bt, xml, src, WriteOptions{
		CompressionType: HdrFlagCompressXPRESS,
		ChunkSize:       32768,
		BootIndex:       0,
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	r, err := NewReader(bytes.NewReader(wimBytes), int64(len(wimBytes)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	hdr := r.Header()
	if hdr.BootIndex != 0 {
		t.Fatalf("BootIndex = %d, want 0", hdr.BootIndex)
	}
	if !hdr.BootMetadata.IsZero() {
		t.Fatalf("BootMetadata is set, want zero (no boot image)")
	}
	verifyWIM(t, wimBytes, files, 2)
}

func TestWriteToValidation(t *testing.T) {
	files := twoImageFixture()
	images, bt, src := buildTestImages(t, files)
	xml := &XMLData{Document: `<WIM></WIM>`}

	if _, err := Assemble(nil, bt, xml, src, WriteOptions{}); err == nil {
		t.Fatalf("Assemble with no images: want error, got nil")
	}
	if _, err := Assemble(images, bt, xml, src, WriteOptions{BootIndex: 99}); err == nil {
		t.Fatalf("Assemble with out-of-range BootIndex: want error, got nil")
	}
	if _, err := Assemble(images, bt, xml, src, WriteOptions{CompressionType: HdrFlagCompressLZX, ChunkSize: 0}); err == nil {
		t.Fatalf("Assemble with zero chunk size for a compressed type: want error, got nil")
	}
	if _, err := Assemble(images, nil, xml, src, WriteOptions{}); err == nil {
		t.Fatalf("Assemble with nil blob table: want error, got nil")
	}
	if _, err := Assemble(images, bt, nil, src, WriteOptions{}); err == nil {
		t.Fatalf("Assemble with nil xml data: want error, got nil")
	}
	if _, err := Assemble(images, bt, xml, nil, WriteOptions{}); err == nil {
		t.Fatalf("Assemble with nil blob source: want error, got nil")
	}
}

func TestWriteToRandomGUID(t *testing.T) {
	files := twoImageFixture()
	images1, bt1, src1 := buildTestImages(t, files)
	images2, bt2, src2 := buildTestImages(t, files)
	xml := &XMLData{Document: `<WIM><IMAGE INDEX="1"><NAME>a</NAME></IMAGE><IMAGE INDEX="2"><NAME>b</NAME></IMAGE></WIM>`}

	w1, err := Assemble(images1, bt1, xml, src1, WriteOptions{})
	if err != nil {
		t.Fatalf("Assemble 1: %v", err)
	}
	w2, err := Assemble(images2, bt2, xml, src2, WriteOptions{})
	if err != nil {
		t.Fatalf("Assemble 2: %v", err)
	}
	r1, err := NewReader(bytes.NewReader(w1), int64(len(w1)))
	if err != nil {
		t.Fatalf("NewReader 1: %v", err)
	}
	r2, err := NewReader(bytes.NewReader(w2), int64(len(w2)))
	if err != nil {
		t.Fatalf("NewReader 2: %v", err)
	}
	if r1.Header().GUID == r2.Header().GUID {
		t.Fatalf("two independently-assembled WIMs got the same random GUID")
	}
}

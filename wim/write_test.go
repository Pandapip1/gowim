package wim

import (
	"bytes"
	"crypto/sha1"
	"math/rand"
	"testing"
)

// buildMinimalWIM hand-assembles a single-image, single-part WIM entirely
// from this package's own primitives (Header, BlobTable, ImageMetadata,
// DirEntry, XMLData), using EncodeResourceData to compress each file's
// blob. This is the same technique used to verify the encoder against a
// real, independent decoder (wimlib-imagex extract -- see wim.go and
// README.md for that one-time verification's results); here it is re-used as
// a permanent, hermetic test of the full write path against this package's
// own Reader.
func buildMinimalWIM(t *testing.T, files map[string][]byte, ctype CompressionType, chunkSize uint32, version uint32) []byte {
	t.Helper()

	var buf []byte
	buf = append(buf, make([]byte, HeaderSize)...)

	root := &DirEntry{
		Attributes: FileAttributeDirectory,
		SecurityID: SecurityIDNone,
		Streams:    []Stream{{}},
	}

	type blobInfo struct {
		hash Hash
		res  ResourceHeader
	}
	var blobs []blobInfo

	for name, data := range files {
		hash := Hash(sha1.Sum(data))
		payload, flags, err := EncodeResourceData(data, ctype, chunkSize)
		if err != nil {
			t.Fatalf("EncodeResourceData(%s): %v", name, err)
		}
		res := ResourceHeader{
			SizeInWIM:        uint64(len(payload)),
			Flags:            flags,
			OffsetInWIM:      uint64(len(buf)),
			UncompressedSize: uint64(len(data)),
		}
		buf = append(buf, payload...)
		blobs = append(blobs, blobInfo{hash: hash, res: res})
		if _, err := root.Add(name, hash); err != nil {
			t.Fatalf("Add(%s): %v", name, err)
		}
	}

	im := &ImageMetadata{Security: &SecurityData{}, Root: root}
	metaBytes, err := im.AppendTo(nil)
	if err != nil {
		t.Fatalf("ImageMetadata.AppendTo: %v", err)
	}
	metaHash := Hash(sha1.Sum(metaBytes))
	metaRes := ResourceHeader{
		SizeInWIM:        uint64(len(metaBytes)),
		Flags:            ResFlagMetadata,
		OffsetInWIM:      uint64(len(buf)),
		UncompressedSize: uint64(len(metaBytes)),
	}
	buf = append(buf, metaBytes...)

	bt := &BlobTable{}
	for _, b := range blobs {
		bt.Entries = append(bt.Entries, BlobDescriptor{Resource: b.res, PartNumber: 1, RefCount: 1, Hash: b.hash})
	}
	bt.Entries = append(bt.Entries, BlobDescriptor{Resource: metaRes, PartNumber: 1, RefCount: 1, Hash: metaHash})
	btBytes, err := bt.AppendTo(nil)
	if err != nil {
		t.Fatalf("BlobTable.AppendTo: %v", err)
	}
	btRes := ResourceHeader{SizeInWIM: uint64(len(btBytes)), OffsetInWIM: uint64(len(buf)), UncompressedSize: uint64(len(btBytes))}
	buf = append(buf, btBytes...)

	xml := &XMLData{Document: `<WIM><IMAGE INDEX="1"><NAME>test</NAME></IMAGE></WIM>`}
	xmlBytes := xml.AppendTo(nil)
	xmlRes := ResourceHeader{SizeInWIM: uint64(len(xmlBytes)), OffsetInWIM: uint64(len(buf)), UncompressedSize: uint64(len(xmlBytes))}
	buf = append(buf, xmlBytes...)

	hdr := Header{
		Magic: Magic, Version: version, Flags: HdrFlagCompression | ctype,
		ChunkSize: chunkSize, PartNumber: 1, TotalParts: 1, ImageCount: 1,
		BlobTable: btRes, XMLData: xmlRes, BootMetadata: metaRes, BootIndex: 1,
	}
	hdrBytes, err := hdr.AppendTo(nil)
	if err != nil {
		t.Fatalf("Header.AppendTo: %v", err)
	}
	copy(buf[0:HeaderSize], hdrBytes)
	return buf
}

// TestEncodeResourceDataFullWIMRoundTrip hand-assembles a minimal WIM using
// EncodeResourceData for each of the three compression types, then reads it
// back with this package's own Reader (NewReader, BlobTable, ImageMetadata,
// ReadFile) and confirms every file's contents match exactly. This is the
// same construction independently verified against the real wimlib-imagex
// extract during development (see README.md); this test re-verifies the
// same construction hermetically against this package's own reader on every
// run.
func TestEncodeResourceDataFullWIMRoundTrip(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	random := make([]byte, 5000)
	rnd.Read(random)

	repeat := make([]byte, 100000)
	for i := range repeat {
		repeat[i] = byte(i % 137)
	}

	files := map[string][]byte{
		"text.txt":   bytes.Repeat([]byte("hello world "), 5000),
		"repeat.bin": repeat,
		"random.bin": random,
		"tiny.bin":   {0xAB},
	}

	cases := []struct {
		name      string
		ctype     CompressionType
		chunkSize uint32
		version   uint32
	}{
		{"xpress", HdrFlagCompressXPRESS, 32768, VersionDefault},
		{"lzx", HdrFlagCompressLZX, 32768, VersionDefault},
		{"lzms", HdrFlagCompressLZMS, 131072, VersionSolid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wimBytes := buildMinimalWIM(t, files, tc.ctype, tc.chunkSize, tc.version)

			r, err := NewReader(bytes.NewReader(wimBytes), int64(len(wimBytes)))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			bt, err := r.BlobTable()
			if err != nil {
				t.Fatalf("BlobTable: %v", err)
			}
			meta := bt.MetadataResources()
			if len(meta) != 1 {
				t.Fatalf("MetadataResources: got %d, want 1", len(meta))
			}
			im, err := r.ImageMetadata(meta[0])
			if err != nil {
				t.Fatalf("ImageMetadata: %v", err)
			}
			for name, want := range files {
				got, err := r.ReadFile(im.Root, bt, name)
				if err != nil {
					t.Fatalf("ReadFile(%s): %v", name, err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("ReadFile(%s): mismatch (got %d bytes, want %d bytes)", name, len(got), len(want))
				}
			}
		})
	}
}

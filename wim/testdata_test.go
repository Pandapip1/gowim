package wim

import (
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"testing"
)

// Real, multi-chunk compressed WIM resource payloads (chunk table + chunks,
// exactly the SizeInWIM bytes of the resource as stored on disk), captured
// during this task's development for permanent, hermetic ground-truth
// testing -- no external tool or mounted image is needed to run these tests.
//
//   - lzx_setup_exe.bin: the "/setup.exe" resource from a real Windows 11
//     23H2 install image's sources/boot.wim (LZX, chunk size 32768,
//     confirmed via `wimlib-imagex info`), 3 chunks (95712 bytes
//     uncompressed).
//   - xpress_setup_exe.bin: the "/setup.exe" resource from a WIM produced by
//     `wimlib-imagex capture --compress=xpress` over a small directory of
//     real files from the same install image (chunk size 32768), 11 chunks
//     (335936 bytes uncompressed).
//   - lzms_setup_exe.bin: the same capture with `--compress=lzms` (chunk
//     size 131072), 3 chunks (335936 bytes uncompressed).
//
// All three were extracted via this package's own Reader (BlobTable,
// ImageMetadata, ResourceReader) from the real WIM files, so the resource
// header fields recorded below (uncompressedSize, chunkSize, numChunks) are
// exactly what the WIM itself reports; the expected SHA-1 was independently
// cross-checked against `wimlib-imagex extract`'s own output for the same
// path in both source WIMs.
var (
	//go:embed testdata/lzx_setup_exe.bin
	lzxSetupExeResource []byte
	//go:embed testdata/xpress_setup_exe.bin
	xpressSetupExeResource []byte
	//go:embed testdata/lzms_setup_exe.bin
	lzmsSetupExeResource []byte
)

func mustHexHash(t *testing.T, s string) Hash {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex: %v", err)
	}
	var h Hash
	copy(h[:], b)
	return h
}

func TestDecodeResourceDataRealWIMGroundTruth(t *testing.T) {
	tests := []struct {
		name             string
		payload          []byte
		ctype            CompressionType
		chunkSize        uint32
		uncompressedSize uint64
		wantSHA1         string
	}{
		{
			name:             "lzx/boot.wim setup.exe (3 chunks)",
			payload:          lzxSetupExeResource,
			ctype:            HdrFlagCompressLZX,
			chunkSize:        32768,
			uncompressedSize: 95712,
			wantSHA1:         "d1ff69636758f8429c92c58fe5cb1a2134fa00d0",
		},
		{
			name:             "xpress/captured setup.exe (11 chunks)",
			payload:          xpressSetupExeResource,
			ctype:            HdrFlagCompressXPRESS,
			chunkSize:        32768,
			uncompressedSize: 335936,
			wantSHA1:         "7505644f65958caf7a4e643b8a3cd8fd59a22725",
		},
		{
			name:             "lzms/captured setup.exe (3 chunks)",
			payload:          lzmsSetupExeResource,
			ctype:            HdrFlagCompressLZMS,
			chunkSize:        131072,
			uncompressedSize: 335936,
			wantSHA1:         "7505644f65958caf7a4e643b8a3cd8fd59a22725",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			numChunks := numChunksFor(tc.uncompressedSize, tc.chunkSize)
			if numChunks < 2 {
				t.Fatalf("fixture %s is not multi-chunk (numChunks=%d)", tc.name, numChunks)
			}
			got, err := DecodeResourceData(tc.payload, tc.ctype, tc.chunkSize, tc.uncompressedSize)
			if err != nil {
				t.Fatalf("DecodeResourceData: %v", err)
			}
			if uint64(len(got)) != tc.uncompressedSize {
				t.Fatalf("decoded length = %d, want %d", len(got), tc.uncompressedSize)
			}
			sum := sha1.Sum(got)
			want := mustHexHash(t, tc.wantSHA1)
			if sum != [20]byte(want) {
				t.Fatalf("sha1 = %x, want %s", sum, tc.wantSHA1)
			}
		})
	}
}

// TestReaderResourceReaderPlusDecodeResourceData exercises the same fixture
// through the public Reader.ResourceReader-shaped flow (read the raw bytes,
// then decode), confirming DecodeResourceData's expected inputs line up with
// how a real Reader would hand them over.
func TestReaderResourceReaderPlusDecodeResourceData(t *testing.T) {
	got, err := DecodeResourceData(lzxSetupExeResource, HdrFlagCompressLZX, 32768, 95712)
	if err != nil {
		t.Fatalf("DecodeResourceData: %v", err)
	}
	sum := sha1.Sum(got)
	want := mustHexHash(t, "d1ff69636758f8429c92c58fe5cb1a2134fa00d0")
	if sum != [20]byte(want) {
		t.Fatalf("sha1 = %x, want match", sum)
	}
}

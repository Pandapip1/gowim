package lzms

import (
	"bytes"
	_ "embed"
	"math/rand"
	"testing"
)

// testdataPlaintext and testdataCompressed are a real ground-truth pair:
// testdata/appcompat.xsl is a real file from a Windows 11 23H2 install
// image (sources/appcompat.xsl), and testdata/appcompat.xsl.lzms is the raw
// LZMS-compressed resource bytes wimlib-imagex (the reference
// implementation) actually produced for that exact file when capturing it
// into a WIM with --compress=lzms (single chunk, since 11673 bytes is under
// the 131072-byte WIM LZMS chunk size, so no chunk-table framing is present
// - these are exactly the bytes Decompress must accept). See README.md for
// how this fixture was captured.
var (
	//go:embed testdata/appcompat.xsl
	testdataPlaintext []byte
	//go:embed testdata/appcompat.xsl.lzms
	testdataCompressed []byte
)

func roundTrip(t *testing.T, data []byte) {
	t.Helper()
	compressed := Compress(data)
	if len(compressed)%2 != 0 {
		t.Fatalf("compressed output has odd length %d", len(compressed))
	}
	got, err := Decompress(compressed, len(data))
	if err != nil {
		t.Fatalf("Decompress failed: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("round trip mismatch: input len %d, output len %d", len(data), len(got))
	}
}

func TestRoundTripEmpty(t *testing.T) {
	roundTrip(t, nil)
}

func TestRoundTripSingleByte(t *testing.T) {
	roundTrip(t, []byte{0x42})
	roundTrip(t, []byte{0x00})
	roundTrip(t, []byte{0xFF})
}

func TestRoundTripFewBytes(t *testing.T) {
	roundTrip(t, []byte("ab"))
	roundTrip(t, []byte("abc"))
	roundTrip(t, []byte("abcd"))
}

func TestRoundTripHighlyRepetitive(t *testing.T) {
	data := bytes.Repeat([]byte("ABCD"), 50000) // 200000 bytes, extremely compressible
	roundTrip(t, data)
}

func TestRoundTripAllZeros(t *testing.T) {
	data := make([]byte, 300000)
	roundTrip(t, data)
}

func TestRoundTripRandomIncompressible(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	data := make([]byte, 65536)
	r.Read(data)
	roundTrip(t, data)
}

func TestRoundTripChunkBoundary(t *testing.T) {
	const chunkSize = 131072 // conventional WIM LZMS chunk size
	r := rand.New(rand.NewSource(2))
	for _, size := range []int{chunkSize - 1, chunkSize, chunkSize + 1} {
		data := make([]byte, size)
		r.Read(data)
		// Mix in some repetition so both literals and matches get exercised.
		for i := 1000; i < len(data); i++ {
			if i%7 == 0 {
				data[i] = data[i-997]
			}
		}
		roundTrip(t, data)
	}
}

func TestRoundTripTextLike(t *testing.T) {
	text := bytes.Repeat([]byte(
		"The quick brown fox jumps over the lazy dog. "+
			"Pack my box with five dozen liquor jugs. "+
			"LZMS is a Microsoft compression format used by WIM and ESD files.\n"), 2000)
	roundTrip(t, text)
}

func TestRoundTripBinaryLike(t *testing.T) {
	data := make([]byte, 50000)
	x := uint32(12345)
	for i := range data {
		// A simple LCG to generate reproducible pseudo-binary structured data
		// with runs and patterns, distinct from pure random noise.
		x = x*1103515245 + 12345
		data[i] = byte(x >> 16)
	}
	roundTrip(t, data)
}

func TestDecompressRejectsBadLength(t *testing.T) {
	if _, err := Decompress([]byte{1, 2, 3}, 10); err == nil {
		t.Fatal("expected error for odd-length compressed input")
	}
	if _, err := Decompress([]byte{1, 2}, 10); err == nil {
		t.Fatal("expected error for too-short compressed input")
	}
}

// TestDecompressRealWimlibGroundTruth decodes real LZMS bytes produced by
// wimlib's own encoder (not this package's Compress) and checks the result
// against the real original file content - the actual point of this test:
// proving this package's bitstream understanding matches the real format,
// not just that Compress and Decompress agree with each other.
func TestDecompressRealWimlibGroundTruth(t *testing.T) {
	got, err := Decompress(testdataCompressed, len(testdataPlaintext))
	if err != nil {
		t.Fatalf("Decompress of real wimlib-produced data failed: %v", err)
	}
	if !bytes.Equal(got, testdataPlaintext) {
		t.Fatalf("decoded output does not match real plaintext (got %d bytes, want %d bytes)",
			len(got), len(testdataPlaintext))
	}
}

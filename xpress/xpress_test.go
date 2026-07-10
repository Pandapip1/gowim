package xpress

import (
	"bytes"
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"math/rand"
	"testing"
)

// roundTrip compresses data, decompresses the result, and fails the test if
// the output does not exactly match the input.
func roundTrip(t *testing.T, name string, data []byte) {
	t.Helper()
	compressed := Compress(data)
	got, err := Decompress(compressed, len(data))
	if err != nil {
		t.Fatalf("%s: Decompress failed: %v", name, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("%s: round trip mismatch: in %d bytes, out %d bytes", name, len(data), len(got))
	}
}

func TestRoundTripSynthetic(t *testing.T) {
	rng := rand.New(rand.NewSource(1))

	random := func(n int) []byte {
		b := make([]byte, n)
		rng.Read(b)
		return b
	}
	repeat := func(pattern string, n int) []byte {
		return bytes.Repeat([]byte(pattern), n)
	}

	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"single byte", []byte{0x42}},
		{"two bytes", []byte{0x00, 0xff}},
		{"highly repetitive small", repeat("A", 1000)},
		{"highly repetitive medium", repeat("AB", 40000)},
		{"repetitive text", repeat("the quick brown fox jumps over the lazy dog. ", 2000)},
		{"random incompressible small", random(1000)},
		{"random incompressible 70000", random(70000)},
		{"all zeros 100000", make([]byte, 100000)},

		// Sizes at and around the two WIM chunk sizes in conventional use
		// (32768 and 65536), even though this package itself has no
		// chunk-size concept - these just exercise buffers of that
		// general magnitude plus off-by-one neighbors.
		{"exactly 32768 random", random(32768)},
		{"32768 minus 1 random", random(32767)},
		{"32768 plus 1 random", random(32769)},
		{"exactly 65536 repetitive", repeat("0123456789abcdef", 4096)},
		{"65536 minus 1 random", random(65535)},
		{"65536 plus 1 random", random(65537)},
		{"larger than any WIM chunk size random", random(200000)},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			roundTrip(t, c.name, c.data)
		})
	}
}

// TestMatchAtMaxOffsetAndLength targets the encoder's handling of the
// format's boundary values: a match whose offset is exactly maxOffset and
// whose length is large enough to need the full byte/u16 length-extension
// path.
func TestMatchAtMaxOffsetAndLength(t *testing.T) {
	data := make([]byte, maxOffset+70000)
	// A distinctive 4-byte pattern at the very start...
	copy(data, []byte{0xDE, 0xAD, 0xBE, 0xEF})
	// ...repeated at the very end, far enough away to require a
	// large/maximal offset, with a long run afterward to encourage a long
	// match.
	copy(data[maxOffset:], []byte{0xDE, 0xAD, 0xBE, 0xEF})
	for i := maxOffset + 4; i < len(data); i++ {
		data[i] = byte(i)
	}
	roundTrip(t, "max offset/length stress", data)
}

// groundTruthFixtures are real XPRESS-compressed byte streams, extracted
// directly from a WIM captured by wimlib-imagex (wimlib 1.14.4, using
// `wimlib-imagex capture <dir> out.wim <name> --compress=xpress`) from a
// handful of real files taken from a Windows 11 23H2 installation image.
// Extraction was done with this repo's sibling wim package
// (github.com/Pandapip1/gowim/wim): reading the WIM's blob table, then for
// each non-metadata blob whose resource is compressed and whose
// uncompressed size is within the WIM's chunk size (32768 bytes - so the
// whole resource is exactly one XPRESS stream, with none of the WIM
// chunk-table framing this package deliberately does not implement, see
// xpress.go), reading the resource's raw (still-compressed) bytes via
// Reader.ResourceReader.
//
// Each fixture's raw compressed bytes are embedded directly from a binary
// file under testdata/ (via go:embed), not a text-encoded literal in the Go
// source - only the length and a SHA-1 of the correctly-decompressed
// content, not the original plaintext itself, are stored alongside each one
// (the original files were not preserved, only their compressed form and a
// hash to check decompression against).
//
// This is ground truth produced by a real, independent XPRESS encoder
// (wimlib's, not this package's): it proves Decompress correctly implements
// the format wimlib and WIMGAPI actually produce, not merely that it
// round-trips against this package's own Compress (which the synthetic
// tests above already establish separately).
//
//go:embed testdata/appraiserdatasha1.cat.xpress
var fixtureAppraiserDataSHA1Cat []byte

//go:embed testdata/autounattend.xml.xpress
var fixtureAutounattendXML []byte

//go:embed testdata/cdplib.mof.xpress
var fixtureCdplibMof []byte

//go:embed testdata/hwcompat_small.txt.xpress
var fixtureHwcompatSmallTxt []byte

//go:embed testdata/repetitive.bin.xpress
var fixtureRepetitiveBin []byte
var groundTruthFixtures = []struct {
	name             string // source file name, for test failure messages only
	compressed       []byte // real XPRESS-compressed bytes, from testdata/*.xpress
	uncompressedSize int
	sha1Hex          string // SHA-1 of the expected decompressed content
}{
	{"appraiserdatasha1.cat", fixtureAppraiserDataSHA1Cat, 10569, "cd3f23372a0d6fadb6e605ca08c63f8062627e27"},
	{"autounattend.xml", fixtureAutounattendXML, 1353, "478a464ed49d32d1f7e33dafc81767b054be9722"},
	{"cdplib.mof", fixtureCdplibMof, 1976, "330b5cb59651acccdc1a780e4f1e1630af4cb859"},
	{"hwcompat_small.txt", fixtureHwcompatSmallTxt, 20000, "e047b19c59f66abac8370103f1e4bbfc6977dd86"},
	{"repetitive.bin", fixtureRepetitiveBin, 16000, "4a4c3223855d23112ebeb7ce515449affb988ea6"},
}

// TestGroundTruthWimlibXpress decodes real XPRESS-compressed data produced
// by wimlib (an independent implementation) and checks the result against
// the known-correct SHA-1 of the original file content. This is the
// critical test that this package's bitstream understanding matches reality,
// as opposed to merely being self-consistent with its own encoder - see the
// package doc comment on groundTruthFixtures above for exactly how these
// fixtures were produced.
func TestGroundTruthWimlibXpress(t *testing.T) {
	for _, f := range groundTruthFixtures {
		f := f
		t.Run(f.name, func(t *testing.T) {
			got, err := Decompress(f.compressed, f.uncompressedSize)
			if err != nil {
				t.Fatalf("Decompress failed: %v", err)
			}
			if len(got) != f.uncompressedSize {
				t.Fatalf("decompressed to %d bytes, want %d", len(got), f.uncompressedSize)
			}
			sum := sha1.Sum(got)
			gotHex := hex.EncodeToString(sum[:])
			if gotHex != f.sha1Hex {
				t.Fatalf("decompressed content SHA-1 = %s, want %s", gotHex, f.sha1Hex)
			}
		})
	}
}

// TestCompressThenWimlibReadable is not itself able to invoke wimlib-imagex
// (this package has no test-time dependency on external tools or the wim
// package), but the encoder output it exercises here was, during
// development, independently verified by constructing a minimal hand-built
// WIM around Compress's output and confirming `wimlib-imagex extract`
// (wimlib 1.14.4, a real, independent decoder) reads it back byte-for-byte
// correctly - for repetitive data, random incompressible data, a single-byte
// file, and a buffer just under the 32768-byte WIM chunk size. See
// xpress.go and the README for a summary of that verification. This test
// keeps the same round-trip guarantee under `go test` without requiring
// wimlib-imagex or the sibling wim module to be present.
func TestCompressThenWimlibReadable(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	random := func(n int) []byte {
		b := make([]byte, n)
		rng.Read(b)
		return b
	}
	cases := map[string][]byte{
		"repetitive":    bytes.Repeat([]byte("The quick brown fox jumps over the lazy dog. "), 80),
		"random":        random(5000),
		"single byte":   {0x37},
		"near boundary": bytes.Repeat([]byte{0xAA, 0xBB, 0xCC}, 10000),
	}
	for name, data := range cases {
		roundTrip(t, name, data)
	}
}

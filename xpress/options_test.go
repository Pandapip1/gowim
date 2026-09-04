package xpress

import (
	"bytes"
	"math/rand"
	"testing"
)

// TestCanonicalCodewordsFlatIsIdentity locks in the property compressNone
// relies on: with literal symbols 0-254 at length 8 and both literal 255
// and endOfData at length 9 (flatLens, in huffman.go), canonicalCodewords
// assigns literal byte b the codeword b itself for every b except 255.
// This holds because canonicalCodewords assigns all length-8 codewords
// (0-254) before either length-9 one, and same-length codewords are
// assigned in increasing symbol order starting from 0 -- i.e. plain binary
// counting -- while endOfData (256) gets the second, and only other,
// length-9 codeword.
func TestCanonicalCodewordsFlatIsIdentity(t *testing.T) {
	codewords := canonicalCodewords(flatLens)
	for b := 0; b < numChars-1; b++ {
		if got := codewords[b]; got != uint16(b) {
			t.Fatalf("canonicalCodewords(flatLens)[%d] = %d, want %d", b, got, b)
		}
	}
	// Literal 255 and endOfData share the length-9 class. Per RFC 1951
	// 3.2.2, a length class's first codeword is (count of all shorter
	// codewords) << 1: 255 length-8 codewords shift to 510 (0x1fe), which
	// literal 255 (assigned first, in symbol order) gets; endOfData
	// (256) gets the next one, 511 (0x1ff).
	if got := codewords[numChars-1]; got != 0x1fe {
		t.Fatalf("canonicalCodewords(flatLens)[255] = %#x, want 0x1fe", got)
	}
	if got := codewords[endOfData]; got != 0x1ff {
		t.Fatalf("canonicalCodewords(flatLens)[endOfData] = %#x, want 0x1ff", got)
	}
	// No other match-header symbol has a codeword at all: flatLens gives
	// them all length 0, so canonicalCodewords never assigns them one,
	// and each stays the zero value.
	for sym := numChars; sym < numSymbols; sym++ {
		if sym == endOfData {
			continue
		}
		if got := codewords[sym]; got != 0 {
			t.Fatalf("canonicalCodewords(flatLens)[%d] = %d, want 0 (unused symbol)", sym, got)
		}
	}
}

// roundTripWith is roundTrip, parameterized on the Options used to compress.
func roundTripWith(t *testing.T, name string, data []byte, opts Options) {
	t.Helper()
	compressed := CompressWith(data, opts)
	got, err := Decompress(compressed, len(data))
	if err != nil {
		t.Fatalf("%s: Decompress failed: %v", name, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("%s: round trip mismatch: in %d bytes, out %d bytes", name, len(data), len(got))
	}
}

// TestNoneRoundTrip exercises the same broad shape of inputs as
// TestRoundTripSynthetic, but through the None preset: empty, tiny,
// incompressible, and highly repetitive data must all still round-trip
// correctly even though None deliberately makes no attempt to compress the
// repetitive cases.
func TestNoneRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(2))

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
		{"single byte zero", []byte{0x00}},
		{"single byte max", []byte{0xff}},
		{"two bytes", []byte{0x00, 0xff}},
		{"all 256 byte values", func() []byte {
			b := make([]byte, 256)
			for i := range b {
				b[i] = byte(i)
			}
			return b
		}()},
		{"highly repetitive small", repeat("A", 1000)},
		{"highly repetitive medium", repeat("AB", 40000)},
		{"repetitive text", repeat("the quick brown fox jumps over the lazy dog. ", 2000)},
		{"random incompressible small", random(1000)},
		{"random incompressible 70000", random(70000)},
		{"all zeros 100000", make([]byte, 100000)},
		{"exactly 32768 random", random(32768)},
		{"exactly 65536 repetitive", repeat("0123456789abcdef", 4096)},
		{"larger than any WIM chunk size random", random(200000)},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			roundTripWith(t, c.name, c.data, None())
		})
	}
}

// TestNoneOutputSize checks the overhead None() adds is what its design
// promises: the fixed 256-byte Huffman header, one literal byte's worth of
// bits per input byte (8 bits per literal for all but byte 255, which along
// with the trailing end-of-data marker costs 9 bits each), and slack from
// the bitwriter's coding-unit delay bookkeeping (bitwriter.go), which can
// leave up to one 16-bit unit's worth of padding depending on where the
// last partial unit falls -- so this checks a small window around the
// expected size rather than pinning bitwriter internals.
func TestNoneOutputSize(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	data := make([]byte, 10000)
	rng.Read(data)

	out := CompressWith(data, None())

	base := huffmanHeaderSize + len(data)
	const maxOverhead = 8 // the 9-bit end-of-data marker plus up to two trailing 2-byte coding-unit slots
	if len(out) < base || len(out) > base+maxOverhead {
		t.Fatalf("None() output size = %d, want in [%d, %d] (header %d + literals %d, +/- bitwriter flush slack)",
			len(out), base, base+maxOverhead, huffmanHeaderSize, len(data))
	}
}

// TestDefaultUnchangedByOptions confirms that introducing Options did not
// change Compress's output: Compress(data) must still equal
// CompressWith(data, Options{}) byte for byte, for every caller that never
// heard of Options.
func TestDefaultUnchangedByOptions(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	data := make([]byte, 5000)
	rng.Read(data)
	// Mix in some repetition so the default path's LZ77 matching actually
	// exercises match items, not just literals.
	data = append(data, bytes.Repeat([]byte("repeat-me "), 500)...)

	if got, want := Compress(data), CompressWith(data, Options{}); !bytes.Equal(got, want) {
		t.Fatalf("Compress(data) (%d bytes) != CompressWith(data, Options{}) (%d bytes)", len(got), len(want))
	}
}

// BenchmarkCompressDefaultVsNone compares the default (LZ77 + adaptive
// Huffman) encoder against the None preset on the same repetitive input --
// repetitive data is exactly the case where the default encoder does the
// most match-finding work, so it is the sharpest illustration of how much
// search None skips. Run with -benchmem to see allocations: the default
// path's LZ77 hash-chain match finder and buildLengths's heap allocate
// substantially per call, while compressNone allocates only the output
// buffer.
func BenchmarkCompressDefaultVsNone(b *testing.B) {
	data := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 2000)

	b.Run("Default", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = Compress(data)
		}
	})
	b.Run("None", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = CompressWith(data, None())
		}
	})
}

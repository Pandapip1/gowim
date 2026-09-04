package xpress

import (
	"bytes"
	"math/rand"
	"testing"
)

// TestCanonicalCodewordsFlatIsIdentity locks in the property compressNone
// relies on: with every literal symbol's length set to exactly 8 and every
// match-header symbol's length set to 0 (flatLens, in huffman.go),
// canonicalCodewords assigns literal byte b the codeword b itself. This
// holds because 256 codewords of length 8 exactly saturate the code space
// (256 * 2^-8 == 1, Kraft's inequality with equality), and
// canonicalCodewords assigns same-length codewords in increasing symbol
// order starting from 0 -- i.e. plain binary counting.
func TestCanonicalCodewordsFlatIsIdentity(t *testing.T) {
	codewords := canonicalCodewords(flatLens)
	for b := 0; b < numChars; b++ {
		if got := codewords[b]; got != uint16(b) {
			t.Fatalf("canonicalCodewords(flatLens)[%d] = %d, want %d", b, got, b)
		}
	}
	// No match-header symbol (256-511) has a codeword at all: flatLens
	// gives them all length 0, so canonicalCodewords never assigns them
	// one, and each stays the zero value.
	for sym := numChars; sym < numSymbols; sym++ {
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
// bits per input byte (the flat code is 8 bits per literal, i.e. 1:1 with
// the input), and no end-of-data marker (flatLens[endOfData] is 0, so
// compressNone's trailing writeBits call writes zero bits). The exact
// byte count also depends on the bitwriter's coding-unit delay bookkeeping
// (bitwriter.go), which can leave up to one 16-bit unit's worth of slack
// depending on where the last partial unit falls -- so this checks a small
// window around the exact size rather than pinning bitwriter internals.
func TestNoneOutputSize(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	data := make([]byte, 10000)
	rng.Read(data)

	out := CompressWith(data, None())

	base := huffmanHeaderSize + len(data)
	const maxOverhead = 4 // at most two trailing 2-byte coding-unit slots
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

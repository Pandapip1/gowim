package lzx

import (
	"bytes"
	"testing"
)

// expectedUncompressedSize returns the exact byte length writeUncompressedBlock
// produces for a chunk of dataLen bytes, computed independently from the
// implementation (by reasoning about the format directly, not by calling
// it): a block-type-and-size header (4 bits for the "default size" case,
// or 4 + 16/24 explicit-size bits otherwise) padded out to the next 16-bit
// unit, 12 bytes of recent-offsets fill, dataLen bytes of raw data, and one
// pad byte if dataLen is odd. This is used below to confirm the encoder's
// real output is exactly this fixed-overhead shape -- not merely
// "close to" it -- which is the strongest available proof that no
// match-finding or compression is attempted: a real parse's output size is
// data-dependent, and this isn't.
func expectedUncompressedSize(dataLen int) int {
	var headerBits int
	if dataLen == defaultBlockSize {
		headerBits = 3 + 1
	} else if dataLen > 32768 {
		headerBits = 3 + 1 + 24
	} else {
		headerBits = 3 + 1 + 16
	}
	headerBytes := 2 * ((headerBits + 15) / 16)
	size := headerBytes + 12 + dataLen
	if dataLen&1 != 0 {
		size++
	}
	return size
}

// TestNoneRoundTrip checks that None() round-trips correctly across a range
// of sizes chosen to exercise the format's boundaries: empty, tiny (odd and
// even), exactly one default-size block, just below/above that boundary,
// the 16-bit/24-bit block-size-field boundary (65536), and the largest
// window this package supports at all (maxWindowSize).
func TestNoneRoundTrip(t *testing.T) {
	sizes := []int{0, 1, 2, 11, 100, 32767, 32768, 32769, 65535, 65536, 65537, 200000, maxWindowSize}
	for _, n := range sizes {
		data := make([]byte, n)
		x := uint32(2166136261)
		for i := range data {
			x = x*16777619 ^ uint32(i)
			data[i] = byte(x)
		}

		out := CompressWith(data, None())
		got, err := Decompress(out, n)
		if err != nil {
			t.Fatalf("size %d: Decompress: %v", n, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("size %d: round-trip mismatch", n)
		}

		if n == 0 {
			if len(out) != 0 {
				t.Errorf("size 0: CompressWith(None()) = %d bytes, want 0", len(out))
			}
			continue
		}
		if want := expectedUncompressedSize(n); len(out) != want {
			t.Errorf("size %d: CompressWith(None()) produced %d bytes, want exactly %d (fixed overhead, not a real parse)", n, len(out), want)
		}
	}
}

// TestNoneIgnoresOtherFields checks that Options.Uncompressed takes over the
// whole encode regardless of what else is set alongside it: since compress()
// branches on o.uncompressed before any other field is consulted, every
// other knob is documented as ignored when this is set, and this pins that.
func TestNoneIgnoresOtherFields(t *testing.T) {
	data := bytes.Repeat([]byte("hello world, this compresses just fine"), 500)
	base := CompressWith(data, Options{Uncompressed: true})
	withExtra := CompressWith(data, Options{
		Uncompressed:   true,
		BeamWidth:      16,
		MaxRefineIters: 1,
		DisableDP:      true,
		FullFirstPass:  true,
	})
	if !bytes.Equal(base, withExtra) {
		t.Fatalf("setting other Options fields alongside Uncompressed changed the output (%d vs %d bytes)", len(base), len(withExtra))
	}
}

// TestNoneOutputSizeIsFixedOverhead is a narrower, size-only spot check of
// the same property TestNoneRoundTrip already pins per-size: on real
// (highly compressible) data, every other preset in this package produces
// output far smaller than the input, but None's output tracks input size
// plus a small constant regardless of how compressible the data actually
// is -- direct evidence that no match-finding ever ran.
func TestNoneOutputSizeIsFixedOverhead(t *testing.T) {
	data := bytes.Repeat([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), 1024) // 32768 highly-compressible bytes
	out := CompressWith(data, None())
	want := expectedUncompressedSize(len(data))
	if len(out) != want {
		t.Fatalf("None() on maximally-redundant data produced %d bytes, want %d (fixed overhead)", len(out), want)
	}
	if compressed := CompressWith(data, DefaultOptions()); len(compressed) >= len(out) {
		t.Fatalf("Default's real compression (%d bytes) didn't beat None's raw passthrough (%d) on trivially-compressible data", len(compressed), len(out))
	}
}

// TestBitWriterAlign checks bitWriter.align directly against both of its
// cases: a partial pending unit gets zero-padded to completion (no extra
// unit), while an already-aligned stream (no pending bits) still emits one
// whole extra zero unit -- the write-side mirror of bitReader.align's own
// documented quirk (see its doc in bitreader.go).
func TestBitWriterAlign(t *testing.T) {
	w := newBitWriter()
	w.writeBits(0b101, 3) // 3 pending bits, not aligned
	w.align()
	if len(w.out) != 2 || w.nbits != 0 {
		t.Fatalf("align from a partial unit: out=%d bytes, nbits=%d, want 2 bytes and nbits 0", len(w.out), w.nbits)
	}

	w2 := newBitWriter()
	w2.writeBits(0, 16) // exactly one full unit; now aligned with nothing pending
	w2.align()
	if len(w2.out) != 4 {
		t.Fatalf("align from an already-aligned stream: out=%d bytes, want 4 (2 for the written unit + 2 quirk-discarded)", len(w2.out))
	}
}

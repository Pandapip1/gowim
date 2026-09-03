package lzx

import (
	"math/rand"
	"os"
	"testing"
)

// checkCountBlockBitsMatches asserts countBlockBits (converted to a byte
// length via countBlockBitsToBytes) exactly equals len(encodeBlock(...))
// for the same toks/tables, both without and with an aligned-offset table
// -- the two shapes countBlockBits is meant to replace across the encoder's
// hot comparison-only call sites.
func checkCountBlockBitsMatches(t *testing.T, label string, data []byte, order, nMainSyms int, toks []token) {
	t.Helper()
	slots := tokenOffsetSlots(toks)
	mainLens, lenLens := buildTables(toks, nMainSyms, slots)
	mainCodes := canonicalCodewords(mainLens, maxMainCodewordLen)
	lenCodes := canonicalCodewords(lenLens, maxLenCodewordLen)

	// VERBATIM (no aligned table).
	wantV := len(encodeBlock(data, order, toks, slots, mainLens, lenLens, mainCodes, lenCodes, nil, nil))
	gotV := countBlockBitsToBytes(countBlockBits(len(data), order, toks, slots, mainLens, lenLens, nil))
	if gotV != wantV {
		t.Errorf("%s: VERBATIM countBlockBits byte length = %d, want %d (len(encodeBlock))", label, gotV, wantV)
	}

	// ALIGNED.
	alignedLens, alignedCodes := buildAlignedTable(toks, slots)
	wantA := len(encodeBlock(data, order, toks, slots, mainLens, lenLens, mainCodes, lenCodes, alignedLens, alignedCodes))
	gotA := countBlockBitsToBytes(countBlockBits(len(data), order, toks, slots, mainLens, lenLens, alignedLens))
	if gotA != wantA {
		t.Errorf("%s: ALIGNED countBlockBits byte length = %d, want %d (len(encodeBlock))", label, gotA, wantA)
	}
}

// TestCountBlockBitsMatchesEncodeBlock verifies, across a variety of real
// token streams (random incompressible data, highly repetitive data,
// pseudo-ASCII text, and a real ground-truth WIM chunk), that
// countBlockBits' bit-accounting -- added as a performance optimization so
// callers that only need a byte-length comparison don't have to
// materialize a full bitstream via encodeBlock -- produces byte lengths
// identical to the real encodeBlock path it replaces at those call sites.
func TestCountBlockBitsMatchesEncodeBlock(t *testing.T) {
	mkToks := func(data []byte) (order, nMainSyms int, toks []token) {
		pre := make([]byte, len(data))
		copy(pre, data)
		lzxPreprocess(pre)
		var err error
		order, err = windowOrder(len(pre))
		if err != nil {
			t.Fatalf("windowOrder: %v", err)
		}
		nMainSyms = numMainSyms(order)
		toks1 := findMatches(pre, costModel{})
		mainLens1, lenLens1 := buildTables(toks1, nMainSyms, tokenOffsetSlots(toks1))
		toks = findMatches(pre, costModel{mainLens: mainLens1, lenLens: lenLens1})
		return order, nMainSyms, toks
	}

	cases := []struct {
		name string
		data []byte
	}{
		{"random-incompressible", func() []byte {
			r := rand.New(rand.NewSource(1))
			b := make([]byte, 20000)
			r.Read(b)
			return b
		}()},
		{"all-zeros", make([]byte, 15000)},
		{"repetitive", func() []byte {
			b := make([]byte, 15000)
			for i := range b {
				b[i] = byte(i % 7)
			}
			return b
		}()},
		{"pseudo-ascii-text", pseudoASCIIText(20000, 42)},
		{"mixed-text-then-random", func() []byte {
			first := pseudoASCIIText(15000, 7)
			second := make([]byte, 15000)
			rand.New(rand.NewSource(9)).Read(second)
			return append(first, second...)
		}()},
		{"small", []byte("the quick brown fox jumps over the lazy dog")},
	}

	for _, c := range cases {
		order, nMainSyms, toks := mkToks(c.data)
		checkCountBlockBitsMatches(t, c.name, c.data, order, nMainSyms, toks)
	}

	// A real ground-truth WIM chunk, if available, for extra realism.
	if plain, err := os.ReadFile("testdata/real_uncompressed_block_chunk_plain.bin"); err == nil {
		order, nMainSyms, toks := mkToks(plain)
		checkCountBlockBitsMatches(t, "real-wim-chunk", plain, order, nMainSyms, toks)
	}
}

package pa30

import (
	"encoding/binary"
	"testing"
)

// bitWriter is a test-only encoder mirroring bitReader's conventions
// (LSB-first per byte, numbers via the nibble scheme, buffers via
// size+align+raw-bytes), used to hand-construct synthetic PA30 patches so
// this package's decode pipeline can be tested end-to-end without a real
// PA30 file (which requires a working ground truth this package does not
// yet have -- see doc.go).
type bitWriter struct {
	bits []uint32
}

func (w *bitWriter) writeBit(b uint32) { w.bits = append(w.bits, b&1) }

func (w *bitWriter) writeBits(v uint32, n int) {
	for i := 0; i < n; i++ {
		w.writeBit((v >> uint(i)) & 1)
	}
}

// writeCodeword writes a canonical Huffman codeword's bits MSB-first (the
// order bitReader/huffmanTree.decode consumes them in).
func (w *bitWriter) writeCodeword(code uint32, length int) {
	for i := length - 1; i >= 0; i-- {
		w.writeBit((code >> uint(i)) & 1)
	}
}

func (w *bitWriter) writeNumber(v uint32) {
	nibbles := 1
	for nibbles < 8 && v >= (uint32(1)<<uint(nibbles*4)) {
		nibbles++
	}
	for i := 0; i < nibbles-1; i++ {
		w.writeBit(0)
	}
	w.writeBit(1)
	w.writeBits(v, nibbles*4)
}

// alignToByte pads with zero bits so the next bit written lands at a byte
// boundary relative to the eventual packed stream (which is prefixed with
// a 3-bit pad-count field), mirroring bitReader.alignToByte.
func (w *bitWriter) alignToByte() {
	pos := 3 + len(w.bits)
	if pos%8 != 0 {
		for i := 0; i < 8-pos%8; i++ {
			w.writeBit(0)
		}
	}
}

func (w *bitWriter) writeBuffer(content []byte) {
	w.writeNumber(uint32(len(content)))
	w.alignToByte()
	for _, by := range content {
		w.writeBits(uint32(by), 8)
	}
}

// finish packs the accumulated bits into bytes, prefixed with the 3-bit
// pad-count field every independent PA30 bitstream begins with, padding the
// final byte with zero bits as needed.
func (w *bitWriter) finish() []byte {
	total := 3 + len(w.bits)
	pad := (8 - total%8) % 8
	all := make([]uint32, 0, total+pad)
	for i := 0; i < 3; i++ {
		all = append(all, (uint32(pad)>>uint(i))&1)
	}
	all = append(all, w.bits...)
	for i := 0; i < pad; i++ {
		all = append(all, 0)
	}
	data := make([]byte, len(all)/8)
	for i, b := range all {
		if b != 0 {
			data[i/8] |= 1 << uint(i%8)
		}
	}
	return data
}

// canonicalCodeword independently walks the same canonical-Huffman
// bookkeeping decode() uses (first/index per length) to find the codeword
// assigned to sym under lens, so tests can hand-construct bitstreams that
// this package's own decoder will interpret correctly. This does not
// bypass verification of the Huffman engine itself -- that's covered by
// TestHuffmanDecodeHandComputedCodes, which checks decode() against
// independently, manually computed codewords.
func canonicalCodeword(t *testing.T, lens []int, maxLen int, sym int) (code uint32, length int) {
	t.Helper()
	tree, err := buildHuffmanTree(lens, maxLen)
	if err != nil {
		t.Fatalf("buildHuffmanTree: %v", err)
	}
	first, idx := 0, 0
	for l := 1; l <= maxLen; l++ {
		count := tree.counts[l]
		for k := 0; k < count; k++ {
			if tree.symbols[idx+k] == sym {
				return uint32(first + k), l
			}
		}
		idx += count
		first = (first + count) << 1
	}
	t.Fatalf("symbol %d not found in tree", sym)
	return 0, 0
}

// TestDecodeSyntheticNullDelta hand-builds a minimal, valid, null-delta
// PA30 file (empty preprocessing, empty base rift table, default Huffman
// lengths) encoding a literal 'a' followed by a DST back-reference match
// (offset 1, length 3), which should decode to "aaaa" -- exercising the
// full pipeline: header parsing, buffer extraction, the patch buffer's
// rift-table/compression-parameter flags, default-length tree
// construction, and both the literal and DST match content paths.
//
// This is a self-built synthetic file, not a real Windows-produced one --
// see doc.go's verification status note.
func TestDecodeSyntheticNullDelta(t *testing.T) {
	want := "aaaa"

	mainLens := defaultLengths(mainTreeSize)

	pw := &bitWriter{}
	pw.writeBit(0) // base rift table: empty
	pw.writeBit(1) // isDefault: use default Huffman lengths

	// Literal 'a' (0x61 = 97): main-tree symbol == literal byte value.
	code, length := canonicalCodeword(t, mainLens, maxCodeLen, 'a')
	pw.writeCodeword(code, length)

	// DST match: slot 8 (offset = slot-7 = 1), lenField 2 (length = lenField+1 = 3).
	// sym = 256 + slot*8 + lenField.
	const slot, lenField = 8, 2
	matchSym := 256 + slot*8 + lenField
	code, length = canonicalCodeword(t, mainLens, maxCodeLen, matchSym)
	pw.writeCodeword(code, length)
	// slot 8-10 and a nonzero lenField need no further bits (see match.go).

	patchBuf := pw.finish()

	ow := &bitWriter{}
	ow.writeNumber(0)                 // FileTypeSet
	ow.writeNumber(0)                 // FileType
	ow.writeNumber(0)                 // Flags
	ow.writeNumber(uint32(len(want))) // TargetSize
	ow.writeNumber(0)                 // TargetHashAlgID (0 = unrecognized, not verified)
	ow.writeBuffer(nil)               // TargetHash (empty; alg unrecognized so unchecked)
	ow.writeBuffer(nil)               // preProcessBuffer (empty)
	ow.writeBuffer(patchBuf)          // patchBuffer

	outer := ow.finish()

	data := make([]byte, 0, 12+len(outer))
	data = append(data, []byte("PA30")...)
	var timeBuf [8]byte
	binary.LittleEndian.PutUint64(timeBuf[:], 0x01cd456789abcdef)
	data = append(data, timeBuf[:]...)
	data = append(data, outer...)

	out, h, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if string(out) != want {
		t.Errorf("Decode = %q, want %q", out, want)
	}
	if h.TargetSize != uint32(len(want)) {
		t.Errorf("Header.TargetSize = %d, want %d", h.TargetSize, len(want))
	}
	if h.TargetFileTime != 0x01cd456789abcdef {
		t.Errorf("Header.TargetFileTime = %#x, want %#x", h.TargetFileTime, 0x01cd456789abcdef)
	}
}

// TestDecodeRejectsNonEmptyRiftTable checks that a patch buffer whose base
// rift table isNonEmpty bit is set (a real, non-null-delta patch) is
// rejected with an explicit error, per this package's stated scope, rather
// than silently misdecoding.
func TestDecodeRejectsNonEmptyRiftTable(t *testing.T) {
	pw := &bitWriter{}
	pw.writeBit(1) // base rift table: non-empty
	patchBuf := pw.finish()

	ow := &bitWriter{}
	ow.writeNumber(0)
	ow.writeNumber(0)
	ow.writeNumber(0)
	ow.writeNumber(0)
	ow.writeNumber(0)
	ow.writeBuffer(nil)
	ow.writeBuffer(nil)
	ow.writeBuffer(patchBuf)
	outer := ow.finish()

	data := make([]byte, 0, 12+len(outer))
	data = append(data, []byte("PA30")...)
	data = append(data, make([]byte, 8)...)
	data = append(data, outer...)

	_, _, err := Decode(data)
	if err == nil {
		t.Fatal("Decode succeeded on a non-empty rift table, want an error")
	}
}

func TestDecodeRejectsBadSignature(t *testing.T) {
	if _, _, err := Decode([]byte("NOTAPATCH...")); err == nil {
		t.Fatal("Decode succeeded on bad signature, want an error")
	}
}

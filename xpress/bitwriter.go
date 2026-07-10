package xpress

import "encoding/binary"

// bitWriter implements the peculiar interleaved bit/byte output stream used
// by XPRESS's LZ77+Huffman variant.
//
// The compressed item stream is not a plain bitstream: Huffman-coded symbol
// headers and "extra offset bits" are packed MSB-first into 16-bit
// little-endian coding units, while match length extensions (a raw byte
// and/or a raw 16-bit value) are written directly as bytes, interleaved with
// those coding units. To make streaming decode possible with a single
// forward-reading cursor, a compliant encoder must delay each completed
// 16-bit coding unit by two coding-unit "slots": at any time there are two
// reserved (but not yet finalized) slots ahead of the point at which raw
// bytes are being appended, and a coding-unit flush fills the oldest
// reserved slot while opening a new one immediately behind the current raw
// byte cursor.
//
// This is not a simplification or a choice of ours: it is bit-for-bit the
// scheme implemented by wimlib's xpress_output_bitstream (xpress_compress.c,
// commit cd5e231c348c255ae5088873b5a66ee0eb96fa07), and matching it exactly
// is required for the output to be decodable by any conforming XPRESS
// decoder (this package's own decoder included, since it mirrors wimlib's
// input_bitstream just as precisely — see bitreader.go and decode.go).
type bitWriter struct {
	out []byte

	bitBuf   uint32
	bitCount uint32

	nextBits  int // index of the oldest not-yet-written coding-unit slot
	nextBits2 int // index of the second-oldest not-yet-written slot
	nextByte  int // index at which the next raw byte/coding unit is appended
}

// newBitWriter creates a bitWriter that appends to out (which must already
// hold at least 4 bytes reserved for the first two coding units; xpress_write
// (encode.go) always starts a fresh output buffer at the appropriate offset).
func newBitWriter(out []byte, start int) *bitWriter {
	w := &bitWriter{
		out:       out,
		nextBits:  start,
		nextBits2: start + 2,
		nextByte:  start + 4,
	}
	w.growTo(w.nextByte)
	return w
}

// growTo ensures len(w.out) >= n, zero-extending as needed.
func (w *bitWriter) growTo(n int) {
	if len(w.out) >= n {
		return
	}
	if cap(w.out) >= n {
		w.out = w.out[:n]
		return
	}
	grown := make([]byte, n)
	copy(grown, w.out)
	w.out = grown
}

// writeBits writes the low numBits bits of bits (MSB of the field first) into
// the coding-unit stream. At most 16 bits may be written in a single call.
func (w *bitWriter) writeBits(bits uint32, numBits uint32) {
	if numBits == 0 {
		return
	}
	w.bitCount += numBits
	w.bitBuf = (w.bitBuf << numBits) | bits

	if w.bitCount > 16 {
		w.bitCount -= 16
		val := uint16(w.bitBuf >> w.bitCount)
		w.growTo(w.nextBits + 2)
		binary.LittleEndian.PutUint16(w.out[w.nextBits:], val)

		w.nextBits = w.nextBits2
		w.nextBits2 = w.nextByte
		w.growTo(w.nextByte + 2)
		w.nextByte += 2
	}
}

// writeByte interleaves a single literal byte directly into the output
// stream at the current raw-byte cursor.
func (w *bitWriter) writeByte(b byte) {
	w.growTo(w.nextByte + 1)
	w.out[w.nextByte] = b
	w.nextByte++
}

// writeU16 interleaves a little-endian 16-bit value directly into the output
// stream at the current raw-byte cursor.
func (w *bitWriter) writeU16(v uint16) {
	w.growTo(w.nextByte + 2)
	binary.LittleEndian.PutUint16(w.out[w.nextByte:], v)
	w.nextByte += 2
}

// flush writes out the final (possibly partial) coding unit and the second
// still-pending reserved slot, and returns the total number of bytes
// written.
func (w *bitWriter) flush() []byte {
	val := uint16(w.bitBuf << (16 - w.bitCount))
	w.growTo(w.nextBits + 2)
	binary.LittleEndian.PutUint16(w.out[w.nextBits:], val)

	w.growTo(w.nextBits2 + 2)
	binary.LittleEndian.PutUint16(w.out[w.nextBits2:], 0)

	w.growTo(w.nextByte)
	return w.out[:w.nextByte]
}

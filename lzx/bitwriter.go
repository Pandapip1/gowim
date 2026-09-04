package lzx

// bitWriter writes an LZX bitstream: bits are packed into little-endian
// 16-bit coding units, with the bits of each unit written from high to low
// (MSB first) in the order they are added -- the mirror image of bitReader.
// See bitreader.go for the general convention and its source.
type bitWriter struct {
	out   []byte
	buf   uint64
	nbits uint
}

func newBitWriter() *bitWriter {
	return &bitWriter{}
}

// newBitWriterCap is newBitWriter with a capacity hint for the output
// buffer, to avoid some of writeBits' incremental append reallocation when
// the caller already knows roughly how large the output will be (LZX
// output is essentially never much larger than its input).
func newBitWriterCap(hint int) *bitWriter {
	return &bitWriter{out: make([]byte, 0, hint)}
}

// writeBits appends the low n bits of value (n <= 32) to the stream, most
// significant of those n bits first.
func (w *bitWriter) writeBits(value uint32, n uint) {
	if n == 0 {
		return
	}
	v := uint64(value) & ((uint64(1) << n) - 1)
	w.buf = (w.buf << n) | v
	w.nbits += n
	for w.nbits >= 16 {
		w.nbits -= 16
		word := uint16(w.buf >> w.nbits)
		w.out = append(w.out, byte(word), byte(word>>8))
	}
	// Keep buf bounded to the still-pending bits so it never overflows.
	if w.nbits < 64 {
		w.buf &= (uint64(1) << w.nbits) - 1
	}
}

// writeRawBytes appends literal bytes directly to the output, bypassing bit
// packing entirely. Used for the UNCOMPRESSED block body (and its
// recent-offsets fill) after align, which is what actually establishes the
// 16-bit coding-unit boundary this needs to be meaningful.
func (w *bitWriter) writeRawBytes(b []byte) {
	w.out = append(w.out, b...)
}

// align realigns the stream to the next 16-bit coding-unit boundary, after
// which writeRawBytes may be used to write literal bytes directly. This is
// the write-side mirror of bitReader.align, and reproduces the same
// documented LZX format quirk that method does (see its doc, and wimlib's
// own src/lzx_decompress.c comment: "if the stream is *already* aligned,
// the correct thing to do is to throw away the next 16 bits (this is
// probably a mistake in the format)"): if no bits are currently pending
// (the stream already sits on a coding-unit boundary), one whole extra
// all-zero 16-bit unit is still emitted here, matching bitReader.align's
// ensure(1) forcing a fresh (and then discarded) fetch in that same case.
// If bits are pending, they are simply zero-padded out to complete the
// current unit, exactly as flush does for a final partial unit.
func (w *bitWriter) align() {
	if w.nbits == 0 {
		w.out = append(w.out, 0, 0)
		return
	}
	word := uint16(w.buf << (16 - w.nbits))
	w.out = append(w.out, byte(word), byte(word>>8))
	w.nbits = 0
	w.buf = 0
}

// writeRawU32 writes v as a little-endian 32-bit integer directly to the
// output, bypassing bit packing. Used for the UNCOMPRESSED block's
// recent-offsets queue fill (see uncompressed.go), mirroring
// bitReader.readU32.
func (w *bitWriter) writeRawU32(v uint32) {
	w.out = append(w.out, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

// flush finalizes the bitstream, writing any partially-filled final coding
// unit (padded with zero low bits), matching lzx_flush_output().
func (w *bitWriter) flush() []byte {
	if w.nbits != 0 {
		word := uint16(w.buf << (16 - w.nbits))
		w.out = append(w.out, byte(word), byte(word>>8))
		w.nbits = 0
		w.buf = 0
	}
	return w.out
}

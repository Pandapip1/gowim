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

// align pads the stream with zero bits up to the next 16-bit coding-unit
// boundary and writes literal bytes directly, mirroring the WIM
// uncompressed-block header/body convention (which is always used
// immediately after a fresh alignment point in this package's encoder).
func (w *bitWriter) writeRawBytes(b []byte) {
	w.out = append(w.out, b...)
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

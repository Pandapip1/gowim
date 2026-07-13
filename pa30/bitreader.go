package pa30

import "fmt"

// bitReader reads bits LSB-first from a byte slice, per PA30's convention:
// byte k occupies stream-bit-positions [8k, 8k+8), and within a byte, bit 0
// (the LSB) is the earliest bit position of that byte. Multi-bit reads
// return a value whose bit 0 is the earliest-read stream bit (i.e. reads
// are not byte-swapped or reversed across byte boundaries).
//
// Each independent PA30 bitstream (the outer stream, and separately each
// buffer's content when parsed as a nested bitstream) begins with a 3-bit
// field giving the number of padding bits present in that stream's last
// byte; bitReader reads and stores this at construction but does not
// currently enforce it as a hard read boundary (see doc.go's verification
// status note -- this is deliberately permissive pending real ground
// truth).
type bitReader struct {
	data []byte
	pos  int // next bit position to read, in stream-bit-position units
	pad  int // padding bits in the stream's last byte, from the 3-bit prefix
}

// newBitReader creates a bitReader over data and consumes the leading 3-bit
// padding-count prefix every independent PA30 bitstream starts with.
func newBitReader(data []byte) (*bitReader, error) {
	br := &bitReader{data: data}
	pad, err := br.readBits(3)
	if err != nil {
		return nil, fmt.Errorf("pa30: read bitstream pad prefix: %w", err)
	}
	br.pad = int(pad)
	return br, nil
}

// readBits reads the next n bits (0 <= n <= 32), returning them as an
// unsigned value whose bit 0 is the earliest-read stream bit.
func (br *bitReader) readBits(n int) (uint32, error) {
	if n < 0 || n > 32 {
		return 0, fmt.Errorf("pa30: invalid bit count %d", n)
	}
	var v uint32
	for i := 0; i < n; i++ {
		bytePos := br.pos >> 3
		if bytePos >= len(br.data) {
			return 0, fmt.Errorf("pa30: read past end of bitstream")
		}
		bitPos := uint(br.pos & 7)
		bit := (br.data[bytePos] >> bitPos) & 1
		v |= uint32(bit) << uint(i)
		br.pos++
	}
	return v, nil
}

// readBit reads a single bit as a 0/1 value.
func (br *bitReader) readBit() (uint32, error) {
	return br.readBits(1)
}

// readNumber decodes a PA30 variable-length integer: a run of zero bits
// terminated by a 1 bit (the run length plus the terminating bit is called
// "nibbles" below, capped at 8), followed by nibbles*4 literal value bits
// (read as-is, LSB-first, no implicit leading bit -- unlike true
// Elias-gamma coding). Verified directly against the README's own worked
// example in bitreader_test.go.
func (br *bitReader) readNumber() (uint32, error) {
	nibbles := 0
	for {
		bit, err := br.readBit()
		if err != nil {
			return 0, err
		}
		nibbles++
		if bit == 1 || nibbles >= 8 {
			break
		}
	}
	return br.readBits(nibbles * 4)
}

// alignToByte discards any remaining bits in the current byte, advancing to
// the start of the next byte boundary. Used before reading a raw (buffer)
// byte span embedded in the bitstream.
func (br *bitReader) alignToByte() {
	if br.pos%8 != 0 {
		br.pos += 8 - br.pos%8
	}
}

// readRawBytes reads n raw bytes directly; the reader must already be
// byte-aligned (see alignToByte).
func (br *bitReader) readRawBytes(n int) ([]byte, error) {
	if br.pos%8 != 0 {
		return nil, fmt.Errorf("pa30: readRawBytes called while not byte-aligned")
	}
	if n < 0 {
		return nil, fmt.Errorf("pa30: negative raw byte count %d", n)
	}
	bytePos := br.pos >> 3
	if bytePos+n > len(br.data) {
		return nil, fmt.Errorf("pa30: raw byte read past end of buffer")
	}
	out := br.data[bytePos : bytePos+n]
	br.pos += n * 8
	return out, nil
}

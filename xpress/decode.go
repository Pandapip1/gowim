package xpress

import "fmt"

// Decompress decodes an XPRESS LZ77+Huffman compressed buffer produced by
// Compress (or by any other conforming XPRESS encoder, such as wimlib or
// WIMGAPI). expectedSize is the exact size of the decompressed output; it
// must be known ahead of time (from the WIM resource header, in the
// container-format caller), since -- like wimlib's decoder -- this decoder
// stops as soon as it has produced that many bytes rather than looking for
// an explicit end marker.
func Decompress(data []byte, expectedSize int) ([]byte, error) {
	if expectedSize < 0 {
		return nil, fmt.Errorf("xpress: negative expected size %d", expectedSize)
	}
	if expectedSize == 0 {
		return []byte{}, nil
	}
	if len(data) < huffmanHeaderSize {
		return nil, fmt.Errorf("xpress: compressed data too short: need at least %d bytes for the Huffman header, have %d", huffmanHeaderSize, len(data))
	}

	lens := unpackHuffmanLengths(data[:huffmanHeaderSize])
	dec := buildHuffmanDecoder(lens)

	r := newBitReader(data[huffmanHeaderSize:])
	out := make([]byte, 0, expectedSize)

	for len(out) < expectedSize {
		sym, ok := dec.decode(r)
		if !ok {
			return nil, fmt.Errorf("xpress: invalid Huffman codeword at output offset %d", len(out))
		}
		if sym < numChars {
			out = append(out, byte(sym))
			continue
		}

		lenHdr := sym & 0xf
		log2Offset := (sym >> 4) & 0xf

		r.ensureBits(16)
		offset := (1 << uint(log2Offset)) | int(r.popBits(uint32(log2Offset)))

		length := lenHdr
		if length == 0xf {
			length += int(r.readByte())
			if length == 0xf+0xff {
				length = int(r.readU16())
			}
		}
		length += minMatchLen

		if offset <= 0 || offset > len(out) {
			return nil, fmt.Errorf("xpress: match offset %d invalid at output offset %d", offset, len(out))
		}
		remaining := expectedSize - len(out)
		if length > remaining {
			// A conforming encoder never emits a match that overruns
			// expectedSize; if this happens the compressed data is
			// inconsistent with expectedSize.
			return nil, fmt.Errorf("xpress: match length %d overruns expected size %d at output offset %d", length, expectedSize, len(out))
		}
		src := len(out) - offset
		for k := 0; k < length; k++ {
			out = append(out, out[src+k])
		}
	}

	return out, nil
}

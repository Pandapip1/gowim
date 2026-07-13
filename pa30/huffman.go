package pa30

import (
	"fmt"
	"math/bits"
)

// maxCodeLen is the maximum code length (in bits) for PA30's main, length,
// and aligned-offset trees, per the README ("all three trees use max. 16
// bit long codes").
const maxCodeLen = 16

// huffmanTree is a canonical Huffman decode table built from a per-symbol
// code-length array: symbols of equal length are assigned consecutive codes
// in increasing symbol-index order (the same canonical ordering DEFLATE
// uses), confirmed to match the PA30 reference decoder's construction (see
// doc.go for provenance) -- PA30's "left-leaning" note describes decode
// bit-consumption direction, not a different canonical ordering.
type huffmanTree struct {
	counts  []int // counts[l] = number of symbols with code length l
	symbols []int // symbols ordered by (length, then symbol index)
	maxLen  int
}

// buildHuffmanTree constructs a decode table from lens (lens[sym] = code
// length in bits, 0 = symbol unused). maxLen bounds the longest code length
// this tree may use.
func buildHuffmanTree(lens []int, maxLen int) (*huffmanTree, error) {
	counts := make([]int, maxLen+1)
	for _, l := range lens {
		if l < 0 || l > maxLen {
			return nil, fmt.Errorf("pa30: huffman code length %d exceeds max %d", l, maxLen)
		}
		if l > 0 {
			counts[l]++
		}
	}
	// offsets[l] = starting index in symbols[] for length-l symbols.
	offsets := make([]int, maxLen+2)
	for l := 1; l <= maxLen; l++ {
		offsets[l+1] = offsets[l] + counts[l]
	}
	total := offsets[maxLen+1]
	symbols := make([]int, total)
	next := append([]int(nil), offsets[:maxLen+1]...)
	for sym, l := range lens {
		if l > 0 {
			symbols[next[l]] = sym
			next[l]++
		}
	}
	return &huffmanTree{counts: counts, symbols: symbols, maxLen: maxLen}, nil
}

// decode reads one Huffman-coded symbol from br, using the standard
// canonical-code decode algorithm (equivalent to zlib's puff.c inflate
// reference): each successive codeword bit is read from the bit reader and
// folded into a growing prefix as its new low bit.
func (t *huffmanTree) decode(br *bitReader) (int, error) {
	code, first, index := 0, 0, 0
	for l := 1; l <= t.maxLen; l++ {
		bit, err := br.readBit()
		if err != nil {
			return 0, err
		}
		code |= int(bit)
		count := t.counts[l]
		if code-first < count {
			return t.symbols[index+(code-first)], nil
		}
		index += count
		first += count
		first <<= 1
		code <<= 1
	}
	return 0, fmt.Errorf("pa30: invalid huffman code (no symbol matched within %d bits)", t.maxLen)
}

// defaultLengths returns the "default" (uniform-weight) code lengths for a
// complete canonical Huffman tree over size symbols, used when a PA30
// compression-parameter block sets isDefault: for size<=2 every symbol gets
// length 1; otherwise let len = floor(log2(size-1))+1, and the lowest
// (1<<len)-size symbols (by index) get length len-1 while the remaining,
// higher-indexed symbols get length len.
func defaultLengths(size int) []int {
	lens := make([]int, size)
	if size <= 2 {
		for i := range lens {
			lens[i] = 1
		}
		return lens
	}
	length := bits.Len(uint(size - 1)) // floor(log2(size-1))+1
	shorter := (1 << uint(length)) - size
	for i := 0; i < shorter; i++ {
		lens[i] = length - 1
	}
	for i := shorter; i < size; i++ {
		lens[i] = length
	}
	return lens
}

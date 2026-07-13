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
// code-length array. Symbols are still assigned to array slots in the
// standard, increasing (length, then symbol index) order, but the
// per-length code-value threshold (first) is computed top-down (from the
// longest length down to the shortest, via a halving recurrence) rather
// than DEFLATE's bottom-up (shortest-to-longest, doubling) recurrence --
// PA30's numerically smallest code values belong to its LONGEST code
// length, the reverse of DEFLATE's canonical assignment. This was
// determined empirically: the bottom-up (DEFLATE-style) construction this
// package originally used decoded a real WinSxS `.manifest` file's first
// content symbol incorrectly (a nonsensical match at output position 0);
// switching only this threshold computation to top-down, and correspondingly
// switching decode's match test to a two-sided range check (see decode
// below), reproduced the correct symbol sequence (confirmed against real
// data via a background research agent that independently re-derived and
// ran the reference decoder's actual construction -- see TODO.md's PA30
// verification entries for the full trail). This is a real, confirmed
// difference from DEFLATE, not merely a decode bit-direction quirk.
type huffmanTree struct {
	counts  []int // counts[l] = number of symbols with code length l
	symbols []int // symbols ordered by (length, then symbol index)
	first   []int // first[l] = smallest l-bit code value assigned to length l
	start   []int // start[l] = index into symbols[] where length-l's group begins
	maxLen  int
}

// buildHuffmanTree constructs a decode table from lens (lens[sym] = code
// length in bits, 0 = symbol unused). maxLen bounds the longest code length
// this tree may use.
func buildHuffmanTree(lens []int, maxLen int) (*huffmanTree, error) {
	counts := make([]int, maxLen+2) // counts[maxLen+1] stays 0, a convenient sentinel
	for _, l := range lens {
		if l < 0 || l > maxLen {
			return nil, fmt.Errorf("pa30: huffman code length %d exceeds max %d", l, maxLen)
		}
		if l > 0 {
			counts[l]++
		}
	}

	// start[l] = starting index in symbols[] for length-l symbols, in the
	// standard increasing-length layout (shorter lengths occupy the front
	// of the array). This part is unchanged from a textbook construction.
	start := make([]int, maxLen+2)
	for l := 1; l <= maxLen; l++ {
		start[l+1] = start[l] + counts[l]
	}
	total := start[maxLen+1]
	symbols := make([]int, total)
	next := append([]int(nil), start[:maxLen+1]...)
	for sym, l := range lens {
		if l > 0 {
			symbols[next[l]] = sym
			next[l]++
		}
	}

	// first[l]: PA30's top-down threshold recurrence (longest length first,
	// starting at 0, halving as length decreases) -- see the type doc above.
	first := make([]int, maxLen+1)
	sum := 0
	for l := maxLen; l >= 1; l-- {
		first[l] = sum
		sum = (sum + counts[l]) >> 1
	}

	return &huffmanTree{counts: counts, symbols: symbols, first: first, start: start, maxLen: maxLen}, nil
}

// decode reads one Huffman-coded symbol from br. Because first[] is built
// top-down (see buildHuffmanTree), a growing code prefix is not guaranteed
// to stay >= first[l] as length increases the way it would under a
// textbook bottom-up construction, so both bounds of the per-length range
// must be checked (code in [first[l], first[l]+counts[l])), not just an
// upper-bound difference.
func (t *huffmanTree) decode(br *bitReader) (int, error) {
	code := 0
	for l := 1; l <= t.maxLen; l++ {
		bit, err := br.readBit()
		if err != nil {
			return 0, err
		}
		code = (code << 1) | int(bit)
		count := t.counts[l]
		if count > 0 && code >= t.first[l] && code-t.first[l] < count {
			return t.symbols[t.start[l]+(code-t.first[l])], nil
		}
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

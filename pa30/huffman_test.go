package pa30

import "testing"

func TestDefaultLengths(t *testing.T) {
	cases := []struct {
		size int
		want []int
	}{
		// size<=2: everything gets length 1.
		{1, []int{1}},
		{2, []int{1, 1}},
		// size=5: len = floor(log2(4))+1 = 3; shorter = (1<<3)-5 = 3
		// symbols get length 2, remaining 2 get length 3.
		{5, []int{2, 2, 2, 3, 3}},
		// size=8 (power of 2): len = floor(log2(7))+1 = 3; shorter = 8-8 = 0,
		// so every symbol gets length 3.
		{8, []int{3, 3, 3, 3, 3, 3, 3, 3}},
		// size=16 (aligned-offset tree's real size): len=floor(log2(15))+1=4;
		// shorter=16-16=0 -> all length 4.
		{16, []int{4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4}},
	}
	for _, c := range cases {
		got := defaultLengths(c.size)
		if len(got) != len(c.want) {
			t.Fatalf("size %d: len(got)=%d, want %d", c.size, len(got), len(c.want))
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("size %d: lens[%d] = %d, want %d", c.size, i, got[i], c.want[i])
			}
		}
	}
}

// TestHuffmanDecodeHandComputedCodes builds a tree from a lengths array
// chosen by hand (not derived from this package's own code) and verifies
// decode() against manually computed canonical codewords, to check the
// Huffman engine independent of any PA30-specific semantics.
//
// Symbols 0,1,2 have length 2; symbols 3,4 have length 3. Standard
// canonical-code assignment (increasing length, then increasing symbol
// index, codes numerically increasing per length) gives:
//
//	symbol 0 -> "00"   symbol 1 -> "01"   symbol 2 -> "10"
//	symbol 3 -> "110"  symbol 4 -> "111"
//
// (Kraft sum check: 3*(1/4) + 2*(1/8) = 1.0, a complete code.)
func TestHuffmanDecodeHandComputedCodes(t *testing.T) {
	lens := []int{2, 2, 2, 3, 3}
	tree, err := buildHuffmanTree(lens, 3)
	if err != nil {
		t.Fatalf("buildHuffmanTree: %v", err)
	}

	// Codewords, MSB first (the order decode() consumes bits in): 00 01 10 110 111
	bitStream := []uint32{
		0, 0, // symbol 0: "00"
		0, 1, // symbol 1: "01"
		1, 0, // symbol 2: "10"
		1, 1, 0, // symbol 3: "110"
		1, 1, 1, // symbol 4: "111"
	}
	br := newTestBitWriterReader(t, bitStream)

	wantSymbols := []int{0, 1, 2, 3, 4}
	for _, want := range wantSymbols {
		got, err := tree.decode(br)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got != want {
			t.Errorf("decode = %d, want %d", got, want)
		}
	}
}

// newTestBitWriterReader packs bits (each 0 or 1, in MSB-first codeword
// read order, matching how decode() consumes them: the first element is
// the first bit read) into bytes using this package's LSB-first-per-byte
// stream convention, prefixed with a 3-bit zero pad field, and returns a
// bitReader over the result.
func newTestBitWriterReader(t *testing.T, bits []uint32) *bitReader {
	t.Helper()
	all := append([]uint32{0, 0, 0}, bits...) // 3-bit zero pad prefix
	nBytes := (len(all) + 7) / 8
	data := make([]byte, nBytes)
	for i, b := range all {
		if b != 0 {
			data[i/8] |= 1 << uint(i%8)
		}
	}
	br, err := newBitReader(data)
	if err != nil {
		t.Fatalf("newBitReader: %v", err)
	}
	return br
}

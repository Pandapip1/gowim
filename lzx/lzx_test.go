package lzx

import (
	"bytes"
	"math/rand"
	"testing"
)

// TestBuildLengthsSingleUsedSymbolIsComplete guards against a real bug found
// and fixed during gowim/wim's write-side integration testing (2026-07-10):
// when exactly one symbol in an alphabet had nonzero frequency, buildLengths
// used to assign it codeword length 1 and leave every other symbol at length
// 0. That is a valid *prefix* code but an *incomplete* one (Kraft sum 1/2,
// not 1), and real WIM data produced this way was rejected outright by
// wimlib's decoder: wimlib's make_huffman_decode_table
// (src/decompress_common.c) explicitly rejects any incomplete code unless it
// is completely empty (no symbols used at all) -- confirmed by direct calls
// into libwim's wimlib_decompress during development, and reproduced
// end-to-end via wimlib-imagex extract on a hand-assembled WIM.
//
// wimlib's own encoder (src/compress_common.c,
// make_canonical_huffman_code) handles the single-used-symbol case by
// assigning a second, otherwise-unused codeword (symbol 0, or symbol 1 if
// the real symbol is 0) so the code has exactly two length-1 codewords,
// making it complete while staying canonical (the lower-valued symbol still
// gets codeword 0). buildLengths now does the same.
func TestBuildLengthsSingleUsedSymbolIsComplete(t *testing.T) {
	tests := []struct {
		name       string
		usedSymbol int
		n          int
	}{
		{"used symbol nonzero", 5, 16},
		{"used symbol is zero", 0, 16},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			freqs := make([]uint32, tc.n)
			freqs[tc.usedSymbol] = 100
			lens := buildLengths(freqs, 16)

			numLen1 := 0
			for _, l := range lens {
				if l == 1 {
					numLen1++
				}
			}
			if numLen1 != 2 {
				t.Fatalf("expected exactly 2 length-1 codewords (complete code), got %d; lens=%v", numLen1, lens)
			}
			if lens[tc.usedSymbol] != 1 {
				t.Fatalf("used symbol %d has length %d, want 1", tc.usedSymbol, lens[tc.usedSymbol])
			}
			other := 0
			if tc.usedSymbol == 0 {
				other = 1
			}
			if lens[other] != 1 {
				t.Fatalf("expected dummy symbol %d to get length 1, got %d", other, lens[other])
			}
		})
	}
}

func roundTrip(t *testing.T, name string, data []byte) {
	t.Helper()
	compressed := Compress(data)
	got, err := Decompress(compressed, len(data))
	if err != nil {
		t.Fatalf("%s: Decompress error: %v", name, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("%s: round-trip mismatch: got %d bytes, want %d bytes", name, len(got), len(data))
	}
}

func TestRoundTripEmpty(t *testing.T) {
	roundTrip(t, "empty", nil)
}

func TestRoundTripSingleByte(t *testing.T) {
	roundTrip(t, "single byte", []byte{0x42})
	roundTrip(t, "single E8 byte", []byte{0xE8})
}

func TestRoundTripRepetitive(t *testing.T) {
	data := bytes.Repeat([]byte("AB"), 20000) // 40000 bytes, spans the 32768 chunk boundary
	roundTrip(t, "repetitive", data)
}

func TestRoundTripAllZeros(t *testing.T) {
	roundTrip(t, "all zeros", make([]byte, 100000))
}

func TestRoundTripRandomIncompressible(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	data := make([]byte, 50000)
	r.Read(data)
	roundTrip(t, "random", data)
}

func TestRoundTripChunkBoundary(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for _, size := range []int{32767, 32768, 32769, 1, 2, 3, 65535, 65536, 65537} {
		data := make([]byte, size)
		r.Read(data)
		roundTrip(t, "boundary", data)
	}
}

// x86CodeLike synthesizes data resembling x86 machine code with a generous
// sprinkling of E8 (CALL rel32) instructions with plausible-looking relative
// displacements, to exercise the E8 address-translation filter.
func x86CodeLike(n int) []byte {
	r := rand.New(rand.NewSource(3))
	data := make([]byte, n)
	i := 0
	for i < n {
		if i+5 <= n && r.Intn(6) == 0 {
			data[i] = 0xE8
			disp := int32(r.Intn(20000) - 10000)
			data[i+1] = byte(disp)
			data[i+2] = byte(disp >> 8)
			data[i+3] = byte(disp >> 16)
			data[i+4] = byte(disp >> 24)
			i += 5
		} else {
			data[i] = byte(r.Intn(256))
			i++
		}
	}
	return data
}

func TestRoundTripX86Like(t *testing.T) {
	roundTrip(t, "x86-like small", x86CodeLike(2000))
	roundTrip(t, "x86-like chunk", x86CodeLike(32768))
}

func TestDecompressEmptyExpected(t *testing.T) {
	got, err := Decompress([]byte{1, 2, 3}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty result, got %d bytes", len(got))
	}
}

func TestDecompressInvalidData(t *testing.T) {
	// Garbage data that isn't a valid stream for a large expected size
	// should either error or panic-recover into ErrInvalidData, never
	// panic out of Decompress.
	garbage := bytes.Repeat([]byte{0xFF}, 50)
	_, err := Decompress(garbage, 100000)
	if err == nil {
		t.Fatalf("expected an error decoding garbage data")
	}
}

// realDesktopIniLZXChunk is the exact, real, single-chunk LZX-compressed
// bytes of "/Program Files/desktop.ini" from the sources/boot.wim resource
// of a real Windows 11 23H2 install image (tiny11 23H2 x64), extracted
// directly from the WIM resource's byte range (this file is small enough to
// be a single 32768-byte WIM chunk, so no chunk-table framing is involved).
// realDesktopIniPlaintext is the same file's contents, extracted
// independently via `wimlib-imagex extract` (real, independent ground
// truth). Together these verify this package's decoder against real-world
// data produced by Microsoft's WIMGAPI (the file predates and is unrelated
// to wimlib or this package), not just self-consistency with our own
// encoder.
var realDesktopIniLZXChunk = []byte{
	0x0a, 0x20, 0x00, 0xe1, 0x00, 0x00, 0x02, 0x00, 0x00, 0x30, 0x45, 0x60,
	0xb9, 0x6f, 0xbc, 0x71, 0x8a, 0x3b, 0x50, 0xb1, 0x08, 0xae, 0x86, 0x8b,
	0x2b, 0x24, 0xa1, 0x8e, 0x4b, 0x2b, 0xb1, 0x1f, 0x0d, 0xfb, 0xff, 0xbd,
	0xc9, 0x7f, 0x00, 0xd9, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05, 0x08, 0x00,
	0x54, 0xc2, 0xd1, 0x6a, 0x46, 0xa3, 0xe4, 0x45, 0x0b, 0x3f, 0x52, 0xbd,
	0xfe, 0x86, 0x38, 0xfe, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x81, 0x80, 0xff, 0xff, 0xbc, 0xff, 0xae, 0xd2, 0x78, 0x2c, 0x3b, 0x18,
	0xc6, 0x9f, 0xe9, 0x46, 0xa9, 0x72, 0x82, 0xec, 0x63, 0x7e, 0xe1, 0xa7,
	0xd1, 0xe3, 0x74, 0x8c, 0xf8, 0xb0, 0xd8, 0x98, 0x22, 0xd1, 0xe9, 0xbb,
	0xb2, 0xda, 0x88, 0x04, 0xd3, 0x87, 0x3a, 0x19, 0x1c, 0xa8, 0x81, 0x9f,
	0xd1, 0xc4, 0x23, 0x95, 0x14, 0xa2, 0x15, 0x27, 0x52, 0x8f, 0x23, 0x85,
	0x36, 0xca, 0xb4, 0x15, 0x7c, 0xd9, 0xd7, 0xcf, 0x32, 0x26, 0xb6, 0xe5,
	0x52, 0x93, 0xb3, 0x06, 0xbe, 0x71, 0x71, 0xb1, 0x00, 0x60,
}

var realDesktopIniPlaintext = []byte{
	0xff, 0xfe, 0x0d, 0x00, 0x0a, 0x00, 0x5b, 0x00, 0x2e, 0x00, 0x53, 0x00,
	0x68, 0x00, 0x65, 0x00, 0x6c, 0x00, 0x6c, 0x00, 0x43, 0x00, 0x6c, 0x00,
	0x61, 0x00, 0x73, 0x00, 0x73, 0x00, 0x49, 0x00, 0x6e, 0x00, 0x66, 0x00,
	0x6f, 0x00, 0x5d, 0x00, 0x0d, 0x00, 0x0a, 0x00, 0x4c, 0x00, 0x6f, 0x00,
	0x63, 0x00, 0x61, 0x00, 0x6c, 0x00, 0x69, 0x00, 0x7a, 0x00, 0x65, 0x00,
	0x64, 0x00, 0x52, 0x00, 0x65, 0x00, 0x73, 0x00, 0x6f, 0x00, 0x75, 0x00,
	0x72, 0x00, 0x63, 0x00, 0x65, 0x00, 0x4e, 0x00, 0x61, 0x00, 0x6d, 0x00,
	0x65, 0x00, 0x3d, 0x00, 0x40, 0x00, 0x25, 0x00, 0x53, 0x00, 0x79, 0x00,
	0x73, 0x00, 0x74, 0x00, 0x65, 0x00, 0x6d, 0x00, 0x52, 0x00, 0x6f, 0x00,
	0x6f, 0x00, 0x74, 0x00, 0x25, 0x00, 0x5c, 0x00, 0x73, 0x00, 0x79, 0x00,
	0x73, 0x00, 0x74, 0x00, 0x65, 0x00, 0x6d, 0x00, 0x33, 0x00, 0x32, 0x00,
	0x5c, 0x00, 0x73, 0x00, 0x68, 0x00, 0x65, 0x00, 0x6c, 0x00, 0x6c, 0x00,
	0x33, 0x00, 0x32, 0x00, 0x2e, 0x00, 0x64, 0x00, 0x6c, 0x00, 0x6c, 0x00,
	0x2c, 0x00, 0x2d, 0x00, 0x32, 0x00, 0x31, 0x00, 0x37, 0x00, 0x38, 0x00,
	0x31, 0x00, 0x0d, 0x00, 0x0a, 0x00,
}

func TestDecompressRealWIMGroundTruth(t *testing.T) {
	got, err := Decompress(realDesktopIniLZXChunk, len(realDesktopIniPlaintext))
	if err != nil {
		t.Fatalf("Decompress error: %v", err)
	}
	if !bytes.Equal(got, realDesktopIniPlaintext) {
		t.Fatalf("real-data decode mismatch:\n got  %q\n want %q", got, realDesktopIniPlaintext)
	}
}

// TestFindMatchesUsesRepeatOffsetQueue guards the repeat-offset (LRU queue)
// support added to the matcher/encoder (2026-08-18, see gowim's own
// TODO.md): a real, ground-truthed comparison against wimlib's own LZX
// compressor found gowim's encoder producing measurably larger output on
// real data because it never reused the LZX repeat-offset queue, paying
// full offset-encoding cost on every match even when immediately repeating
// the same distance. This constructs data with a distant (large offset,
// i.e. outside the always-free slots 0-3) repeated pattern and checks that
// matches after the first reuse a repeat-offset queue slot.
func TestFindMatchesUsesRepeatOffsetQueue(t *testing.T) {
	pattern := make([]byte, 4000)
	for i := range pattern {
		pattern[i] = byte(i * 7 % 251)
	}
	data := append(append([]byte{}, pattern...), pattern...)
	data = append(data, pattern...)

	toks := findMatches(data, costModel{})
	sawFreshLargeOffset := false
	sawRepeat := false
	for _, tok := range toks {
		if !tok.isMatch {
			continue
		}
		if tok.repeat < 0 && tok.offset >= len(pattern) {
			sawFreshLargeOffset = true
		}
		if tok.repeat >= 0 {
			sawRepeat = true
		}
	}
	if !sawFreshLargeOffset {
		t.Fatalf("expected at least one fresh match at the pattern's repeat distance")
	}
	if !sawRepeat {
		t.Fatalf("expected the repeated pattern to produce repeat-offset matches")
	}
}

// TestRoundTripRepeatOffsetPattern exercises encode/decode correctness for
// data that should trigger heavy repeat-offset queue use (all three queue
// slots), including offsets large enough to require extra offset bits.
func TestRoundTripRepeatOffsetPattern(t *testing.T) {
	a := bytes.Repeat([]byte{0x11, 0x22, 0x33, 0x44, 0x55}, 500) // distinct offset A
	b := bytes.Repeat([]byte{0xAA, 0xBB, 0xCC, 0xDD}, 700)       // distinct offset B
	var data []byte
	// Interleave so the matcher must juggle multiple recent offsets (A, B,
	// A again) rather than just reusing one.
	for i := 0; i < 6; i++ {
		data = append(data, a...)
		data = append(data, b...)
	}
	roundTrip(t, "repeat-offset interleaved", data)
}

// TestCompressAllZerosMatchesWimlibSize guards the precode run-length
// (symbols 17/18) support added to writeCodewordLens (2026-08-18, see
// gowim's own TODO.md): before this fix, an all-zero chunk sent every one
// of its ~496 unused main-alphabet symbols' codeword lengths individually,
// producing 156 compressed bytes for a case where real wimlib -- which
// collapses long runs of unused symbols into a couple of precode run-length
// symbols -- produces exactly 78. This is a strict, exact-value regression
// guard (not a range check) precisely because 78 is real, ground-truthed
// wimlib output for this exact input, not an estimate.
func TestCompressAllZerosMatchesWimlibSize(t *testing.T) {
	data := make([]byte, 32768)
	out := Compress(data)
	const wimlibSize = 78
	if len(out) != wimlibSize {
		t.Fatalf("got %d compressed bytes for an all-zero chunk, want exactly %d (wimlib's real output)", len(out), wimlibSize)
	}
}

// TestFindMatchesNeverSelfMatchesAtPositionZero guards a real bug found
// while adding lazy (one-step lookahead) matching (2026-08-18): inserting a
// position's own hash entry *before* searching for its match let position 0
// match against itself at offset 0 (an immediately invalid match, since a
// match offset must be >= 1) whenever findMatches was restructured to
// insert eagerly to support the lazy peek at i+1. Any repeated short
// pattern reproduces this at position 0.
func TestFindMatchesNeverSelfMatchesAtPositionZero(t *testing.T) {
	data := bytes.Repeat([]byte("AB"), 50)
	toks := findMatches(data, costModel{})
	if len(toks) == 0 {
		t.Fatal("expected at least one token")
	}
	if toks[0].isMatch && toks[0].offset == 0 {
		t.Fatalf("first token is an invalid offset-0 self-match: %+v", toks[0])
	}
	for _, tok := range toks {
		if tok.isMatch && tok.offset <= 0 {
			t.Fatalf("found match with non-positive offset: %+v", tok)
		}
	}
}

// TestCostModelPrefersCheaperOffset guards the cost-aware match selection
// added to chooseMatch (2026-08-18, see gowim's own TODO.md): candidates
// are now ranked by an estimated bit value (length saved minus offset/
// symbol cost) rather than raw match length, so a shorter match at a much
// cheaper offset should be preferred over a longer match at a very
// expensive (many-extra-bits) offset when the value works out that way.
func TestCostModelPrefersCheaperOffset(t *testing.T) {
	m := costModel{}
	// Same match length, different offset slots: value must strictly
	// decrease as the slot's extra-bit cost increases, isolating the
	// offset-cost effect from length.
	cheapSlot := 4   // lzxExtraOffsetBits[4] == 1
	costlySlot := 34 // lzxExtraOffsetBits[34] == 16
	const length = 10
	cheapValue := m.matchValue(cheapSlot, length, int(lzxExtraOffsetBits[cheapSlot]))
	costlyValue := m.matchValue(costlySlot, length, int(lzxExtraOffsetBits[costlySlot]))
	if cheapValue <= costlyValue {
		t.Fatalf("expected cheap-offset match to have higher value at equal length: cheap=%d costly=%d", cheapValue, costlyValue)
	}

	// A length-10 match at a cheap offset should also beat a slightly
	// longer (length-11) match at the costly offset, since the extra-bit
	// cost difference (15 bits) dwarfs one byte's worth of length (8 bits).
	costlyLonger := m.matchValue(costlySlot, length+1, int(lzxExtraOffsetBits[costlySlot]))
	if cheapValue <= costlyLonger {
		t.Fatalf("expected cheap-offset match to beat a slightly longer costly-offset match: cheap=%d costlyLonger=%d", cheapValue, costlyLonger)
	}
}

// TestTwoPassRefinementRunsWithoutPanicking guards the two-pass encode
// (2026-08-18): pass 2's costModel is built from pass 1's Huffman lengths,
// which must be indexable by every main/length symbol pass 2's parse can
// produce (in particular, an all-zero Huffman freq table would leave a
// short mainLens slice if buildLengths ever changed its output size --
// this pins the assumption that mainLens1/lenLens1 are always exactly
// numMainSyms(order)/lenCodeNumSymbols long).
func TestTwoPassRefinementRunsWithoutPanicking(t *testing.T) {
	roundTrip(t, "two-pass small", []byte("hello, world! hello, world! hello, world!"))
	roundTrip(t, "two-pass empty-ish", []byte{0})
	roundTrip(t, "two-pass single-symbol", bytes.Repeat([]byte{0x7A}, 5000))
}

// TestAlignedBlockCanBeSmaller guards the ALIGNED-block trial added to
// compress() (2026-08-18, see gowim's own TODO.md): the encoder now
// encodes each chunk both as VERBATIM and ALIGNED and keeps whichever is
// smaller. This builds a synthetic token stream of fresh matches at large
// offsets (slot >= minAlignedOffsetSlot) whose low 3 extra-offset bits are
// heavily skewed toward one value -- exactly the case a skewed 8-symbol
// aligned Huffman code should beat 3 raw bits per match on -- and checks
// encodeBlock directly produces a smaller ALIGNED encoding for it.
func TestAlignedBlockCanBeSmaller(t *testing.T) {
	order := 15
	nMainSyms := numMainSyms(order)
	var toks []token
	slot := 20 // well above minAlignedOffsetSlot(8)
	base := uint32(lzxOffsetSlotBase[slot])
	for i := 0; i < 500; i++ {
		extra := uint32(0)
		if i%20 == 0 {
			extra = 5 // rare outlier
		}
		toks = append(toks, token{isMatch: true, offset: int(base + extra), length: 10, repeat: -1})
	}

	mainLens, lenLens := buildTables(toks, nMainSyms)
	mainCodes := canonicalCodewords(mainLens, maxMainCodewordLen)
	lenCodes := canonicalCodewords(lenLens, maxLenCodewordLen)

	data := make([]byte, defaultBlockSize) // dummy; only its length is used for the block header
	verbatim := encodeBlock(data, order, toks, mainLens, lenLens, mainCodes, lenCodes, nil, nil)
	alignedLens, alignedCodes := buildAlignedTable(toks)
	aligned := encodeBlock(data, order, toks, mainLens, lenLens, mainCodes, lenCodes, alignedLens, alignedCodes)

	if len(aligned) >= len(verbatim) {
		t.Fatalf("expected ALIGNED encoding to be smaller for skewed low-offset-bits data: verbatim=%d aligned=%d", len(verbatim), len(aligned))
	}
}

func TestCompressExceedsMaxWindow(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for oversized input")
		}
	}()
	Compress(make([]byte, maxWindowSize+1))
}

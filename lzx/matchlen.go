package lzx

import (
	"encoding/binary"
	"math/bits"
)

// commonPrefixLen returns the number of matching leading bytes between
// data[c:c+limit] and data[pos:pos+limit] (the caller guarantees both
// ranges fit within data -- see matchLenCapped/matchLenAt in matcher.go
// and optimal.go, which compute limit as min(len(data)-pos, len(data)-c,
// maxMatchLen)).
//
// Rather than a naive byte-by-byte loop, this compares 8 bytes at a time
// via a single XOR plus math/bits.TrailingZeros64 -- the same "SWAR"
// (SIMD-within-a-register) technique real LZ77 encoders (e.g. zstd's
// ZSTD_count) use for this exact operation, portable to every
// architecture Go supports with no build tags, assembly, or CPU-feature
// checks needed.
//
// An AVX2-based version (using Go 1.26's experimental simd/archsimd
// package, gated behind GOEXPERIMENT=simd) was tried and measured against
// this one on the real 398-chunk/12.4MB ntoskrnl.exe benchmark: even after
// restructuring to defer the AVX2 setup to only genuinely long matches
// (see gowim's own TODO.md for that whole investigation), it remained
// measurably slower than this plain SWAR version, not faster, so it was
// removed rather than kept as an option nobody should actually enable.
//
// Deliberately split into a small fast path (this function -- compare the
// first 8 bytes) plus a separate commonPrefixLenRest for extending an
// actual match past those first 8 bytes. This function is called on
// nearly every candidate comparison in the match finder; the split was
// originally meant to let the compiler inline the fast path, but real
// testing found Go's inliner has a fixed per-call cost floor (~60-72 out
// of its 80 budget) that a function needing to call out at all can't get
// under -- so neither this function nor commonPrefixLenRest is actually
// inlined at their call sites. The split is kept anyway because it's
// still a real structural improvement on its own merits: the common case
// (mismatch within the first 8 bytes, or a short match) never reaches
// commonPrefixLenRest at all.
func commonPrefixLen(data []byte, c, pos, limit int) int {
	if limit < 8 {
		return scalarPrefixLen(data, c, pos, limit)
	}
	x := binary.LittleEndian.Uint64(data[c : c+8])
	y := binary.LittleEndian.Uint64(data[pos : pos+8])
	if diff := x ^ y; diff != 0 {
		return bits.TrailingZeros64(diff) / 8
	}
	return 8 + commonPrefixLenRest(data, c+8, pos+8, limit-8)
}

// scalarPrefixLen is the plain byte-by-byte comparison used only for the
// sub-8-byte tail case, kept as its own function (rather than an inline
// loop in commonPrefixLen) specifically so commonPrefixLen itself stays
// small enough for the compiler to inline it -- see commonPrefixLen's own
// doc for why that matters.
func scalarPrefixLen(data []byte, c, pos, limit int) int {
	l := 0
	for l < limit && data[c+l] == data[pos+l] {
		l++
	}
	return l
}

// commonPrefixLenRest extends a match already confirmed equal for its
// first 8 bytes (see commonPrefixLen above), 8 bytes at a time via XOR +
// math/bits.TrailingZeros64, with a scalar byte loop for the final
// sub-8-byte remainder.
func commonPrefixLenRest(data []byte, c, pos, limit int) int {
	l := 0
	for l+8 <= limit {
		x := binary.LittleEndian.Uint64(data[c+l : c+l+8])
		y := binary.LittleEndian.Uint64(data[pos+l : pos+l+8])
		if diff := x ^ y; diff != 0 {
			return l + bits.TrailingZeros64(diff)/8
		}
		l += 8
	}
	return l + scalarPrefixLen(data, c+l, pos+l, limit-l)
}

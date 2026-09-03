package lzx

// This file ports wimlib's real block-splitting heuristic (see
// lzx_should_end_block / lzx_observe_literal / lzx_observe_match /
// lzx_init_block_split_stats in wimlib's src/lzx_compress.c, read
// directly from wimlib's own source, not guessed) as an alternative to
// trySplitChunk's single, bounded midpoint-only split (encode.go).
// Several other wimlib-inspired hypotheses in gowim's own TODO.md (queue-
// state trajectory count, per-length exhaustiveness, inline aligned
// costs, extra refinement passes) were tried and measured to not help;
// this one targets a genuinely different lever -- block *layout*, not
// parse/cost-model strategy -- that hadn't been tried before.
//
// wimlib's own soft-max block size (100000 bytes) and match-cache-
// overflow triggers are irrelevant here: a WIM chunk (conventionally
// 32768 bytes) is always smaller than wimlib's soft max, and this
// package holds a whole chunk's tokens in memory already, so there is no
// cache to overflow. Only the statistics-driven trigger and the
// minimum-block-size gating (minStatsBlockSize, matching wimlib's own
// MIN_BLOCK_SIZE) apply.
const (
	numLiteralObsTypes = 8
	numMatchObsTypes   = 2
	numSplitObsTypes   = numLiteralObsTypes + numMatchObsTypes

	// numObservationsPerBlockCheck matches wimlib's
	// NUM_OBSERVATIONS_PER_BLOCK_CHECK: the split heuristic is only
	// re-evaluated after this many literal/match observations accumulate.
	numObservationsPerBlockCheck = 400

	// minStatsBlockSize matches wimlib's MIN_BLOCK_SIZE: a split is only
	// considered if both the block ending here and the block starting
	// here would be at least this many bytes.
	minStatsBlockSize = 6500
)

// splitObsCounts tallies observations into wimlib's 10 aggregate
// "observation types" (8 literal buckets + 2 match buckets) rather than
// per-symbol frequencies -- per wimlib's own design rationale: coarse
// aggregates are enough to notice real block-boundary-worthy shifts
// (e.g. ASCII-to-binary, few-matches-to-many-matches) without the cost of
// tracking full per-symbol statistics purely for a split decision.
type splitObsCounts [numSplitObsTypes]uint32

// observeLiteralSplit buckets a literal by its top 2 bits and low 1 bit
// (8 possible buckets), matching wimlib's lzx_observe_literal exactly.
func observeLiteralSplit(counts *splitObsCounts, lit byte) {
	counts[((lit>>5)&0x6)|(lit&1)]++
}

// observeMatchSplit buckets a match as "short" or "long" (length >= 5),
// matching wimlib's lzx_observe_match exactly.
func observeMatchSplit(counts *splitObsCounts, length int) {
	idx := numLiteralObsTypes
	if length >= 5 {
		idx++
	}
	counts[idx]++
}

// shouldEndBlockSplit ports lzx_should_end_block's exact decision rule:
// compare the newly-accumulated observation distribution against the
// cumulative distribution so far (cross-scaled by each other's totals to
// avoid division, exactly as wimlib does), and report "end the block
// here" if they differ by at least 7/8 of the expected total -- using the
// same integer-division order wimlib uses ((numNew*7)/8, then *numCum),
// not a mathematically cleaner but bit-different reordering.
func shouldEndBlockSplit(cum, newCounts *splitObsCounts, numCum, numNew uint32) bool {
	if numCum == 0 {
		return false
	}
	var totalDelta uint64
	for i := 0; i < numSplitObsTypes; i++ {
		expected := uint64(cum[i]) * uint64(numNew)
		actual := uint64(newCounts[i]) * uint64(numCum)
		var delta uint64
		if actual > expected {
			delta = actual - expected
		} else {
			delta = expected - actual
		}
		totalDelta += delta
	}
	threshold := (uint64(numNew) * 7 / 8) * uint64(numCum)
	return totalDelta >= threshold
}

// lzxBlockSplitPoints walks toks in byte order, replicating wimlib's real
// block-splitting algorithm (see the constants/helpers above), and
// returns the byte offsets (relative to the start of the chunk) where a
// new LZX block should begin. Unlike trySplitChunk (a single, bounded
// midpoint-only split), this can return zero, one, or several split
// points, driven by actual content statistics rather than position alone.
func lzxBlockSplitPoints(toks []token, totalLen int) []int {
	if totalLen < 2*minStatsBlockSize {
		return nil
	}

	var cum, newCounts splitObsCounts
	var numCum, numNew uint32
	var splits []int
	pos := 0
	lastSplit := 0

	for _, t := range toks {
		if t.isMatch {
			observeMatchSplit(&newCounts, t.length)
			pos += t.length
		} else {
			observeLiteralSplit(&newCounts, t.literal)
			pos++
		}
		numNew++

		if numNew < numObservationsPerBlockCheck {
			continue
		}

		if pos-lastSplit >= minStatsBlockSize && totalLen-pos >= minStatsBlockSize &&
			shouldEndBlockSplit(&cum, &newCounts, numCum, numNew) {
			splits = append(splits, pos)
			lastSplit = pos
			cum = splitObsCounts{}
			numCum = 0
			newCounts = splitObsCounts{}
			numNew = 0
			continue
		}

		for i := range cum {
			cum[i] += newCounts[i]
		}
		numCum += numNew
		newCounts = splitObsCounts{}
		numNew = 0
	}

	return splits
}

// splitBytesToTokenBoundaries maps each byte offset in splitBytes (must
// be strictly increasing, as lzxBlockSplitPoints produces) to the index
// of the token in toks that starts at or after that offset -- i.e. a
// token-stream index that never falls inside a token, since matches may
// not cross a block boundary (see decode.go's lzCopy). splitBytes are
// assumed at least minStatsBlockSize apart, which is far larger than
// maxMatchLen, so no single token can span more than one requested split
// point.
func splitBytesToTokenBoundaries(toks []token, splitBytes []int) []int {
	if len(splitBytes) == 0 {
		return nil
	}
	var boundaries []int
	pos := 0
	si := 0
	for idx, t := range toks {
		for si < len(splitBytes) && pos >= splitBytes[si] {
			if idx > 0 && idx < len(toks) {
				boundaries = append(boundaries, idx)
			}
			si++
		}
		if si >= len(splitBytes) {
			break
		}
		if t.isMatch {
			pos += t.length
		} else {
			pos++
		}
	}
	return boundaries
}

// trySplitChunkStats attempts encoding data as multiple LZX blocks, split
// at byte offsets chosen by lzxBlockSplitPoints (wimlib's real
// statistics-driven heuristic), as an alternative to trySplitChunk's
// single bounded midpoint split. Each block gets its own Huffman tables
// and its own independent VERBATIM-vs-ALIGNED decision, with each
// subsequent block's tables delta-coded against the previous block's real
// lengths (see writeBlockInto). Returns nil if no valid split points
// exist.
func trySplitChunkStats(data []byte, order, nMainSyms int, toks []token) []byte {
	splitBytes := lzxBlockSplitPoints(toks, len(data))
	boundaries := splitBytesToTokenBoundaries(toks, splitBytes)
	if len(boundaries) == 0 {
		return nil
	}

	tokStart, byteStart := 0, 0
	bytePos := 0
	bi := 0
	var segToks [][]token
	var segData [][]byte
	for idx, t := range toks {
		if bi < len(boundaries) && idx == boundaries[bi] {
			segToks = append(segToks, toks[tokStart:idx])
			segData = append(segData, data[byteStart:bytePos])
			tokStart, byteStart = idx, bytePos
			bi++
		}
		if t.isMatch {
			bytePos += t.length
		} else {
			bytePos++
		}
	}
	segToks = append(segToks, toks[tokStart:])
	segData = append(segData, data[byteStart:])

	w := newBitWriterCap(len(data) + 64)
	prevMainLens := zeroMainLens[:nMainSyms]
	prevLenLens := zeroLenLens
	for i, st := range segToks {
		mainLens, lenLens := buildTables(st, nMainSyms)
		mainCodes := canonicalCodewords(mainLens, maxMainCodewordLen)
		lenCodes := canonicalCodewords(lenLens, maxLenCodewordLen)

		sd := segData[i]
		v := encodeBlock(sd, order, st, mainLens, lenLens, mainCodes, lenCodes, nil, nil)
		alignedLens, alignedCodes := buildAlignedTable(st)
		a := encodeBlock(sd, order, st, mainLens, lenLens, mainCodes, lenCodes, alignedLens, alignedCodes)
		if len(a) >= len(v) {
			alignedLens, alignedCodes = nil, nil
		}

		writeBlockInto(w, sd, order, st, mainLens, prevMainLens, lenLens, prevLenLens, mainCodes, lenCodes, alignedLens, alignedCodes)
		prevMainLens, prevLenLens = mainLens, lenLens
	}
	return w.flush()
}

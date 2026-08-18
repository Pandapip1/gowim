package lzx

// token is one literal or match emitted by the match finder.
type token struct {
	isMatch bool
	literal byte
	offset  int // match distance in bytes (>= 1)
	length  int // match length in bytes ([minMatchLen, maxMatchLen])
	// repeat is the repeat-offset LRU queue slot (0, 1, or 2) this match
	// reuses, or -1 if it uses a fresh, non-recent offset. See the
	// package-level doc for how the queue is tracked and why this matters.
	repeat int
}

const (
	hashBits    = 16
	hashSize    = 1 << hashBits
	minMatch    = 3 // minimum length for a *fresh*-offset match (see below)
	maxChainLen = 96

	// Flat per-symbol bit-cost estimates used by costModel when it has no
	// real Huffman codeword lengths yet (findMatches' first pass -- see
	// compress() in encode.go). These are rough constants, not measured
	// from this chunk's actual data: real codeword lengths for the
	// commonly-used literal/main/length symbols in typical data cluster in
	// roughly this range, so this is "good enough to rank candidates
	// sensibly" for a first pass whose whole purpose is to produce
	// *some* Huffman table for the second, real pass to refine against.
	flatLiteralBits = 8
	flatMainBits    = 8
	flatLenBits     = 8
)

// costModel estimates the bit cost of literals and matches, used to choose
// among candidate matches (including whether a repeat-offset match is
// worth preferring over a longer fresh-offset one) and to compare a
// lazy-matching peek against the current position's best candidate. A zero
// costModel (nil Lens slices) uses the flat estimates above -- this is
// findMatches' first pass. A costModel built from a first pass's actual
// Huffman codeword lengths is used for a refining second pass (see
// compress() in encode.go): this is a standard two-pass technique (parse,
// build a code, re-parse against the real code's costs) for approximating
// the joint parse/code optimization a full iterative optimal parser would
// do, without that full complexity.
//
// Literal cost is always the flat estimate, even in the second pass:
// unlike match costs (which vary hugely with offset slot, 0-17 extra bits),
// real literal codeword lengths vary comparatively little, so refining
// match cost captures the great majority of the benefit for much less
// complexity.
type costModel struct {
	mainLens []byte // nil => flatMainBits/flatLenBits estimates
	lenLens  []byte
}

// matchCost returns the estimated bit cost of a match's main-code symbol
// (and secondary length symbol, if needed), NOT including extra offset
// bits (the caller adds those separately, since they depend only on the
// offset slot, not on any Huffman table).
func (m costModel) matchCost(slot, length int) int {
	lengthField := length - minMatchLen
	header := lengthField
	if header > numPrimaryLens {
		header = numPrimaryLens
	}
	mainBits := flatMainBits
	if m.mainLens != nil {
		mainSym := numChars + slot*numLenHeaders + header
		mainBits = int(m.mainLens[mainSym])
	}
	if header != numPrimaryLens {
		return mainBits
	}
	lenBits := flatLenBits
	if m.lenLens != nil {
		lsym := lengthField - numPrimaryLens
		lenBits = int(m.lenLens[lsym])
	}
	return mainBits + lenBits
}

// matchValue estimates how many bits a match of the given slot/length/
// extraBits saves versus emitting length literals instead, using
// flatLiteralBits per literal. Higher is better; a value <= 0 means the
// match is not worth using at all.
func (m costModel) matchValue(slot, length, extraBits int) int {
	return length*flatLiteralBits - m.matchCost(slot, length) - extraBits
}

// candidateMatch is the single best match (repeat-offset or fresh) found at
// one position, as chosen by chooseMatch below.
type candidateMatch struct {
	found  bool
	offset int
	length int
	repeat int // -1 if fresh
	value  int // costModel.matchValue of this candidate; only valid if found
}

// findMatches runs a lazy LZ77 parse over data using a bounded hash-chain
// match finder for fresh offsets, plus a direct check of the LZX
// repeat-offset LRU queue (the three most recently used match offsets) at
// every position. Per this package's documented encoder scope (see lzx.go
// and compress() in encode.go), this is a one-step lazy parse, not a full
// optimal/DP parse: at each position it finds the best-value match (using
// model to weigh repeat-offset matches, which cost no extra offset bits,
// against fresh-offset matches of different lengths and offset slots --
// see costModel above, which replaces a simpler earlier version of this
// package that used a flat "repeat must be within N bytes of fresh" bonus
// instead of an actual, if approximate, bit-cost comparison), then checks
// whether a higher-value match exists starting one byte later using the
// *same* repeat-offset queue state (valid because emitting a literal never
// changes the queue) -- if so, it emits a literal now and takes the better
// match next iteration, the classic "lazy matching" technique (as used by,
// e.g., zlib's deflate and wimlib's own non-near-optimal compression
// levels), rather than a full iterative bit-cost model over the whole
// chunk.
//
// The repeat-offset queue is tracked exactly as the decoder maintains it
// (see decode.go's recentOffsets handling, itself ported from wimlib's
// lzx_decompress.c): starts at {1, 1, 1}, and after every match, the used
// offset is swapped into slot 0 (a no-op if it was already slot 0), while a
// fresh offset shifts the whole queue. This must stay in lockstep with the
// decoder's transitions since encode.go emits the resulting repeat slot
// (0, 1, or 2) directly as the match's offset-slot symbol, with no explicit
// offset bits.
//
// A match's length is always capped at maxMatchLen (257) by matchLenAt, so
// there is no separate "split a long match" step: a run longer than
// maxMatchLen is simply rediscovered on the next iteration, at which point
// it is a repeat-offset match (queue slot 0), which is exactly as compact.
func findMatches(data []byte, model costModel) []token {
	n := len(data)
	var toks []token
	if n == 0 {
		return toks
	}

	head := make([]int32, hashSize)
	for i := range head {
		head[i] = -1
	}
	prev := make([]int32, n)
	inserted := make([]bool, n)

	hash := func(i int) uint32 {
		v := uint32(data[i]) | uint32(data[i+1])<<8 | uint32(data[i+2])<<16
		return (v * 2654435761) >> (32 - hashBits)
	}

	// insertOnce records position i's hash entry the first time any code
	// path visits it, whether it ends up part of a match or a literal, so a
	// later lazy-matching peek at position i+1 can find i as a candidate.
	// It is idempotent since the lazy peek at i+1 and the real processing
	// of i+1 (once the loop cursor reaches it) would otherwise both try to
	// insert it.
	insertOnce := func(i int) {
		if i+3 > n || inserted[i] {
			return
		}
		inserted[i] = true
		h := hash(i)
		prev[i] = head[h]
		head[h] = int32(i)
	}

	matchLenAt := func(i, j int) int {
		limit := n - i
		if n-j < limit {
			limit = n - j
		}
		if limit > maxMatchLen {
			limit = maxMatchLen
		}
		l := 0
		for l < limit && data[i+l] == data[j+l] {
			l++
		}
		return l
	}

	repeatLenAt := func(i int, off int32) int {
		j := i - int(off)
		if j < 0 {
			return 0
		}
		return matchLenAt(j, i)
	}

	// queue mirrors the decoder's recentOffsets, starting at {1, 1, 1} (see
	// decode.go). A repeat-offset match at position i and queue value off
	// references data[i-off:], which is only valid when off <= i.
	var queue [numRecentOffsets]int32 = [numRecentOffsets]int32{1, 1, 1}

	// chooseMatch finds the best-value match at position i given the
	// current queue, without mutating any state (hash table, chain, or
	// queue), so it is safe to call speculatively for a lazy-matching peek.
	chooseMatch := func(i int, queue [numRecentOffsets]int32) candidateMatch {
		var best candidateMatch

		for k := 0; k < numRecentOffsets; k++ {
			l := repeatLenAt(i, queue[k])
			if l < minMatchLen {
				continue
			}
			v := model.matchValue(k, l, 0)
			if !best.found || v > best.value {
				best = candidateMatch{found: true, offset: int(queue[k]), length: l, repeat: k, value: v}
			}
		}

		if i+3 <= n {
			h := hash(i)
			cand := head[h]
			bestLen := 0
			bestOff := 0
			depth := 0
			for cand >= 0 && depth < maxChainLen {
				c := int(cand)
				l := matchLenAt(c, i)
				if l > bestLen {
					bestLen = l
					bestOff = i - c
				}
				cand = prev[c]
				depth++
			}
			if bestLen >= minMatch {
				slot := offsetSlot(uint32(bestOff))
				extraBits := int(lzxExtraOffsetBits[slot])
				v := model.matchValue(slot, bestLen, extraBits)
				if !best.found || v > best.value {
					best = candidateMatch{found: true, offset: bestOff, length: bestLen, repeat: -1, value: v}
				}
			}
		}

		if best.found && best.value <= 0 {
			return candidateMatch{}
		}
		return best
	}

	advanceQueue := func(m candidateMatch) {
		if m.repeat >= 0 {
			if m.repeat != 0 {
				used := queue[m.repeat]
				queue[m.repeat] = queue[0]
				queue[0] = used
			}
			return
		}
		queue[2] = queue[1]
		queue[1] = queue[0]
		queue[0] = int32(m.offset)
	}

	insertRange := func(start, end int) {
		for p := start; p < end; p++ {
			insertOnce(p)
		}
	}

	i := 0
	for i < n {
		// chooseMatch must run before insertOnce(i): inserting i's own hash
		// entry first would let it match against itself at offset 0.
		m := chooseMatch(i, queue)
		insertOnce(i)

		if m.found && i+1 < n {
			peek := chooseMatch(i+1, queue)
			if peek.found && peek.value > m.value {
				toks = append(toks, token{literal: data[i], repeat: -1})
				i++
				continue
			}
		}

		if !m.found {
			toks = append(toks, token{literal: data[i], repeat: -1})
			i++
			continue
		}

		toks = append(toks, token{isMatch: true, offset: m.offset, length: m.length, repeat: m.repeat})
		advanceQueue(m)
		insertRange(i, i+m.length)
		i += m.length
	}

	return toks
}

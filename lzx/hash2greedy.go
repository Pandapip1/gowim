package lzx

// hash2Candidate is one opportunity to replace two consecutive literal
// tokens (as produced by findMatches, which never itself emits a
// fresh-offset match shorter than minMatch=3 -- see matcher.go's
// bestFreshCandidate) with a length-2 fresh-offset match referencing an
// earlier, identical 2-byte occurrence. tokIdx/tokIdx+1 index the two
// literal tokens in the toks slice greedyApplyHash2 was given; pos is
// their byte offset in data.
type hash2Candidate struct {
	tokIdx     int
	pos        int
	offset     int
	slot       int
	extraBits  int
	litA, litB byte
	matchSym   int
}

// findHash2Candidates scans toks for every pair of consecutive literal
// tokens whose 2-byte value already occurred earlier in data, using a
// direct 65536-entry "most recent occurrence" table -- an exact key, not a
// lossy hash, since a 2-byte pair has exactly 65536 possible values, so
// every candidate found here is real (no false positives to verify). These
// are the only substitutions greedyApplyHash2 can ever make: this is
// deliberately restricted to swapping a single pair of literal tokens for
// one length-2 match, never re-deciding any match findMatches already
// chose, so it can never invalidate an existing match's byte range.
func findHash2Candidates(data []byte, toks []token) []hash2Candidate {
	n := len(data)
	lastPos := make([]int32, 1<<16)
	for i := range lastPos {
		lastPos[i] = -1
	}
	prevOcc := make([]int32, n)
	for p := 0; p+1 < n; p++ {
		key := uint16(data[p]) | uint16(data[p+1])<<8
		prevOcc[p] = lastPos[key]
		lastPos[key] = int32(p)
	}

	var cands []hash2Candidate
	pos := 0
	for i := 0; i+1 < len(toks); i++ {
		t0 := toks[i]
		if !t0.isMatch && !toks[i+1].isMatch {
			if q := prevOcc[pos]; q >= 0 {
				offset := pos - int(q)
				slot := offsetSlot(uint32(offset))
				cands = append(cands, hash2Candidate{
					tokIdx:    i,
					pos:       pos,
					offset:    offset,
					slot:      slot,
					extraBits: int(lzxExtraOffsetBits[slot]),
					litA:      t0.literal,
					litB:      toks[i+1].literal,
					matchSym:  numChars + slot*numLenHeaders, // header 0 => lengthField 0 => length 2 (minMatchLen)
				})
			}
		}
		if t0.isMatch {
			pos += t0.length
		} else {
			pos++
		}
	}
	return cands
}

// hash2CandidateValue returns c's real marginal value (bits saved, may be
// negative) under the given, already-built main Huffman table: the cost of
// the two literal symbols it would remove minus the cost of the match
// symbol (plus its extra offset bits) it would add. Unlike costModel's
// flat/stale estimates, mainLens here is expected to be freshly rebuilt
// from the actual frequency counts as of the caller's current commit state
// (see greedyApplyHash2), so this reflects each already-accepted
// candidate's real effect on the table, including any Kraft-inequality
// lengthening of unrelated codewords -- not just a local 3-symbol guess
// against a table that has never seen any hash2 usage at all.
func hash2CandidateValue(mainLens []byte, c hash2Candidate) int {
	litCost := int(mainLens[c.litA]) + int(mainLens[c.litB])
	matchCost := int(mainLens[c.matchSym]) + c.extraBits
	return litCost - matchCost
}

// greedyApplyHash2 greedily substitutes hash2Candidates into toks, one at a
// time: each round, it rebuilds the real main Huffman table from the
// frequency counts as committed so far, scores every still-available
// candidate (one whose two literal tokens haven't already been consumed by
// an earlier acceptance -- overlapping candidates from a run of 3+
// consecutive literals conflict this way) against that real table, and
// accepts whichever scores highest. It stops as soon as the best remaining
// candidate's value is <= 0, i.e. its real marginal cost would meet or
// exceed its benefit -- the "MC > MB" stopping rule.
//
// This directly answers whether a new alphabet symbol's marginal cost can
// be measured exactly rather than estimated from a stale table (see this
// package's own history/TODO.md: a first hash2 attempt scored every
// candidate against one static pass-2 table that never reflected any
// hash2 usage, measured hash2 as a net regression, and was reverted).
// Every ACCEPTED candidate's effect on the table here is fully real: a
// genuine buildLengths call over the updated counts, so later rounds see
// any knock-on lengthening of unrelated codewords that adding a
// previously-zero-frequency symbol can force. What is NOT exact is the
// score used to choose which candidate to accept *next*: that is scored
// against the table as of the last acceptance, not a hypothetical
// per-candidate "what if we added exactly this one" rebuild for every
// remaining candidate every round -- doing that would cost
// O(candidates^2) full rebuilds instead of O(candidates), which isn't
// justified here given the caller re-measures the real encoded byte count
// anyway (see compressLookahead's established "try both, keep smaller"
// pattern) rather than trusting this pass blindly.
func greedyApplyHash2(data []byte, toks []token, nMainSyms int) []token {
	cands := findHash2Candidates(data, toks)
	if len(cands) == 0 {
		return toks
	}

	mainFreqs, _ := tokenFreqs(toks, nMainSyms)

	consumed := make([]bool, len(toks))
	accepted := make(map[int]hash2Candidate)

	remaining := cands
	for {
		mainLens := buildLengths(mainFreqs, maxMainCodewordLen)

		bestIdx := -1
		bestValue := 0
		live := remaining[:0]
		for _, c := range remaining {
			if consumed[c.tokIdx] || consumed[c.tokIdx+1] {
				continue // drop: an earlier acceptance consumed one of its tokens
			}
			live = append(live, c)
			if v := hash2CandidateValue(mainLens, c); v > bestValue {
				bestValue = v
				bestIdx = len(live) - 1
			}
		}
		remaining = live

		if bestIdx == -1 {
			break
		}

		c := remaining[bestIdx]
		consumed[c.tokIdx] = true
		consumed[c.tokIdx+1] = true
		accepted[c.tokIdx] = c
		mainFreqs[c.litA]--
		mainFreqs[c.litB]--
		mainFreqs[c.matchSym]++
	}

	if len(accepted) == 0 {
		return toks
	}

	out := make([]token, 0, len(toks))
	for i := 0; i < len(toks); i++ {
		if c, ok := accepted[i]; ok {
			out = append(out, token{isMatch: true, offset: c.offset, length: minMatchLen, repeat: -1})
			i++ // skip the second literal token, absorbed into the match
			continue
		}
		out = append(out, toks[i])
	}
	return fixupQueueState(out)
}

// fixupQueueState re-derives every match token's repeat field (and hence
// which main-code slot it encodes as) from the actual repeat-offset LRU
// queue trajectory that results from decoding toks in order, using the
// same applyMatch transition matcher.go's own parse and decode.go's
// decoder both use.
//
// This is required for correctness, not just an optimization: splicing a
// new match token into the middle of an existing token stream (as
// greedyApplyHash2 does) changes the queue's contents at every position
// after the splice, since every match -- fresh or repeat -- updates the
// queue. A later token's `repeat` field (and thus its encoded main symbol)
// was chosen by the original parse against the *original* queue
// trajectory; left unfixed, it can silently reference the wrong offset
// once the queue has been perturbed by an inserted hash2 match. Recomputing
// every token's repeat field against the queue's real, current trajectory
// -- rather than trusting whatever classification the original parse
// assigned -- makes the result correct regardless of how many matches were
// spliced in or where.
//
// As a side effect, this can reclassify a pre-existing *fresh* match as a
// repeat-offset one, when its offset happens to now match a queue slot
// that a spliced-in match shifted into place: that's always a free
// improvement (a repeat-offset match costs strictly less to encode than
// the equivalent fresh one, and decodes to the identical offset), never a
// regression.
func fixupQueueState(toks []token) []token {
	var queue [numRecentOffsets]int32 = [numRecentOffsets]int32{1, 1, 1}
	out := make([]token, len(toks))
	for i, t := range toks {
		if !t.isMatch {
			out[i] = t
			continue
		}
		repeat := -1
		off32 := int32(t.offset)
		for k := 0; k < numRecentOffsets; k++ {
			if queue[k] == off32 {
				repeat = k
				break
			}
		}
		t.repeat = repeat
		out[i] = t
		queue = applyMatch(queue, repeat, t.offset)
	}
	return out
}

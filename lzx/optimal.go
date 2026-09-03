package lzx

// findMatchesOptimal runs a forward shortest-path DP over the whole chunk,
// using the same binary-tree match finder as findMatches (see matcher.go),
// as an attempt at a genuinely more "optimal" parse than the bounded 3-way
// lookahead there.
//
// # Honest scope: a bounded multi-state beam, going beyond wimlib's own
// # single-trajectory parser in one specific way
//
// Earlier versions of this comment claimed wimlib's own near-optimal
// parser (`lzx_find_min_cost_path` in wimlib's src/lzx_compress.c) tracks
// multiple live repeat-offset-queue-state hypotheses per position. That
// claim was never actually checked against wimlib's source and turned out
// to be wrong: wimlib's own doc comment on that function says plainly
// that its handling of the adaptive queue state "is actually only an
// approximation" and that "the algorithm does not solve this problem in
// general; it only looks one step ahead" -- i.e. wimlib itself tracks a
// SINGLE queue trajectory per position, the same simplification this
// package's earlier (pre-beam) revision used. See gowim's own TODO.md for
// the correction and the experiment that led to finding it.
//
// This function's beam (below) genuinely goes beyond that single-
// trajectory baseline: at each position it keeps up to beamWidth distinct
// (cost, queue-state) hypotheses instead of collapsing to the single
// cheapest arrival. A higher-cost path that carries a different, more
// useful queue state is not thrown away outright, as long as it's one of
// the beamWidth cheapest distinct-queue hypotheses at that position.
//
// What this function still does NOT do, unlike wimlib's real algorithm:
// relax every possible match length (it samples a bounded subset instead
// -- see repeatLengthSamples/maxFreshCandidates below). Folding
// ALIGNED-block costs into the cost model inline and running more than a
// fixed two refinement passes were both tried and initially reverted
// (2026-08-18) after measuring a real regression -- but that regression
// turned out to be a real bug in costModel.matchCost/offsetExtraCost (a
// zero-frequency symbol's codeword length of 0 was being read as "0 bits,
// free" instead of "not yet encodable"), not a property of these
// techniques themselves. With that fixed, both are now used: see
// costModel.offsetExtraCost (matcher.go) and refineParseWith
// (encode.go), which this function is now run through just like
// findMatches. Native length-2 (hash2) fresh-match candidates -- see
// hash2Candidate/buildHash2PrevOcc in matcher.go -- are also offered as
// DP edges here, not only in findMatches. See gowim's own TODO.md for the
// full investigation and measured numbers.
//
// # Bounded edge and state counts
//
// Both the number of queue-state hypotheses kept per position (beamWidth)
// and the number of edges considered per hypothesis (see
// repeatLengthSamples and maxFreshCandidates below) are bounded, to keep
// worst-case cost from blowing up on pathological (e.g. highly
// repetitive) input. Real-world measurements of this bounding's actual
// cost/benefit are in gowim's own TODO.md.
//
// findMatchesOptimal is the default-tuned entry point; findMatchesOptimalWith
// takes the resolved caller-facing knobs (see Options in options.go), which
// is what compressOptimal threads through.
func findMatchesOptimal(data []byte, model costModel) []token {
	return findMatchesOptimalWith(data, model, defaultEncodeOptions())
}

func findMatchesOptimalWith(data []byte, model costModel, o encodeOptions) []token {
	n := len(data)
	if n == 0 {
		return nil
	}

	order, _ := windowOrder(n) // n > 0 here, and callers never exceed maxWindowSize
	nMainSyms := numMainSyms(order)
	prevOcc2 := buildHash2PrevOcc(data)

	head := make([]int32, hashSize)
	for i := range head {
		head[i] = -1
	}
	left := make([]int32, n)
	right := make([]int32, n)

	hash := func(i int) uint32 {
		v := uint32(data[i]) | uint32(data[i+1])<<8 | uint32(data[i+2])<<16
		return (v * 2654435761) >> (32 - hashBits)
	}

	matchLenCapped := func(c, pos int) (l, limit int) {
		limit = n - pos
		if n-c < limit {
			limit = n - c
		}
		if limit > maxMatchLen {
			limit = maxMatchLen
		}
		return commonPrefixLen(data, c, pos, limit), limit
	}

	// freshCandidate is one Pareto-frontier (length, offset) pair found by
	// bstSearchAll below: within the frontier, a strictly longer candidate
	// always has a strictly larger (more expensive) offset, so a shorter
	// candidate is only kept if no longer-or-equal candidate has an
	// equal-or-cheaper offset.
	type freshCandidate struct {
		length int
		offset int
	}

	// maxFreshCandidates bounds how many distinct fresh-offset candidates
	// the DP considers per position (beyond this, only the longest
	// candidates are kept -- see bstSearchAll), keeping worst-case DP edge
	// count bounded regardless of how many non-dominated candidates a
	// pathological input could otherwise produce. Widened from 8 to 24
	// (2026-08-18, alongside beamWidth below) after measuring a real,
	// if modest, improvement on the full ntoskrnl.exe benchmark (see
	// gowim's own TODO.md) -- found while investigating why gowim still
	// trailed wimlib's real encoder despite its match discovery being
	// verified non-worse; this alone was not the dominant cause (see the
	// same TODO.md entry for what was). Now the default value of
	// Options.MaxFreshCandidates (options.go) rather than a hard constant:
	// it is one of the knobs the preset ladder turns down for speed.
	maxFreshCandidates := o.maxFreshCandidates

	// bstSearchAll walks the BST rooted at head[hash(pos)] exactly as
	// bstSearch does (matcher.go), but instead of keeping only the single
	// longest match, collects every strictly-longer-than-previous
	// candidate seen along the walk (a Pareto frontier by construction,
	// since the walk only records a candidate when it beats the best
	// length seen so far), capped at maxFreshCandidates (keeping the
	// longest ones if more are found).
	bstSearchAll := func(pos int) []freshCandidate {
		if pos+3 > n {
			return nil
		}
		// Capacity hint: most positions find only a handful of strictly-
		// improving candidates along the walk, well under
		// maxFreshCandidates, so a small fixed hint avoids most of the
		// early reallocations without over-allocating for the common
		// case.
		cands := make([]freshCandidate, 0, 8)
		bestLen := 0
		cur := head[hash(pos)]
		depth := 0
		for cur >= 0 && depth < o.maxChainLen {
			c := int(cur)
			l, limit := matchLenCapped(c, pos)
			if l > bestLen && l >= minMatch {
				bestLen = l
				cands = append(cands, freshCandidate{length: l, offset: pos - c})
				if len(cands) > maxFreshCandidates {
					cands = cands[1:] // drop the shortest kept so far
				}
			}
			if l >= limit || data[c+l] < data[pos+l] {
				cur = right[c]
			} else {
				cur = left[c]
			}
			depth++
		}
		return cands
	}

	bstInsert := func(pos int) {
		if pos+3 > n {
			return
		}
		h := hash(pos)
		cur := head[h]
		ptrLo := &left[pos]
		ptrHi := &right[pos]
		depth := 0
		for cur >= 0 && depth < o.maxChainLen {
			c := int(cur)
			l, limit := matchLenCapped(c, pos)
			if l >= limit || data[c+l] < data[pos+l] {
				*ptrLo = int32(c)
				ptrLo = &right[c]
				cur = right[c]
			} else {
				*ptrHi = int32(c)
				ptrHi = &left[c]
				cur = left[c]
			}
			depth++
		}
		*ptrLo = -1
		*ptrHi = -1
		head[h] = int32(pos)
	}

	// beamWidth bounds how many distinct queue-state hypotheses survive at
	// each position. A higher-cost arrival is kept only if it carries a
	// queue state not already covered by a cheaper arrival, and only if
	// it's among the beamWidth cheapest such distinct-queue arrivals. Now
	// the default value of Options.BeamWidth (options.go) rather than a
	// hard constant, since it multiplies both states kept per position and
	// edges relaxed per state and is thus a direct speed/ratio knob.
	beamWidth := o.beamWidth

	// dpEdge records how one state at one position was reached: either a
	// literal (isMatch false) or a match, plus the predecessor position
	// and which of that predecessor's states it came from.
	type dpEdge struct {
		isMatch   bool
		literal   byte
		offset    int
		length    int
		repeat    int
		from      int
		fromState int
	}

	type dpState struct {
		cost  int
		queue [numRecentOffsets]int32
		edge  dpEdge
	}

	states := make([][]dpState, n+1)
	states[0] = []dpState{{
		cost:  0,
		queue: [numRecentOffsets]int32{1, 1, 1},
		edge:  dpEdge{from: -1, fromState: -1},
	}}

	// mergeState folds a newly-relaxed (cost, queue) arrival into
	// states[to]: if a hypothesis with the same queue state already
	// exists there, it's replaced only if the new arrival is cheaper;
	// otherwise the new hypothesis is added as a distinct candidate --
	// but states[to] is held at no more than beamWidth entries *at insert
	// time*, evicting the currently-most-expensive entry (and only if the
	// arrival is strictly cheaper than it; otherwise the arrival is
	// dropped).
	//
	// # Why bound here rather than after the fact (2026-08-18)
	//
	// This function previously appended unconditionally and left the
	// beamWidth bound to a separate pruneState pass run on a position
	// right before that position became the DP's current position. That
	// is correct but accidentally quadratic-ish: a position accumulates
	// arrivals from up to maxMatchLen+1 predecessor positions x beamWidth
	// states x ~32 edges each *before* it is ever pruned, so this
	// function's linear same-queue scan ran over a slice of 20-212 entries
	// (380 at worst on a sampled real ntoskrnl.exe chunk) instead of the
	// intended <= 10. Profiling measured mergeState+pruneState at ~54% of
	// the encoder's CPU (a further 19% in pruneState's reflect-based
	// sort.Slice), and this function's append at 85% of a measured 9.57 GB
	// of allocation for 256 KB of input. Bounding at insert time makes the
	// scan O(beamWidth) and removes essentially all of that growslice
	// traffic: measured (serial, 32 KiB chunks through Compress) 14.5s ->
	// 10.9s on 544 KiB of mixed corpus (8x32KiB of libLLVM.so.18.1,
	// 8x32KiB of Go runtime source, testdata/hash2_greedy_chunk1.bin), and
	// 22.6s -> 15.4s on a wider 832 KiB 8-file corpus (bash, libc, more
	// libLLVM, /usr/share/dict words, Debian copyright text, net/http
	// source, both testdata chunks).
	//
	// This is NOT exactly equivalent to accumulate-then-prune: an arrival
	// that would have survived the old pruning pass can be evicted here by
	// a cheaper arrival, and equal-cost arrivals are resolved differently
	// (the old code's sort.Slice was unstable), so the surviving beam --
	// and hence which of several equal-DP-cost parses is emitted -- can
	// differ. The beam was always a heuristic bound (it is the documented
	// gap between this parser and a true optimal one), so the question is
	// empirical and it was measured rather than assumed: total output
	// moved 134280 -> 134290 bytes (+0.007%) on the first corpus and
	// 255474 -> 255516 (+0.016%) on the second, individual files moving in
	// both directions (llvm_a.bin 17762 -> 17760, bash.bin 30182 ->
	// 30190). That residual is tie-break noise, not a systematic loss:
	// flipping only the tie rule below from `<` to `<=` moves the same
	// 8-file total to 255486 (+0.005%) with no other change. Two
	// alternatives were also measured and rejected: keeping a 2*beamWidth
	// insert-time slack plus a cheap concrete-typed selection prune at the
	// position's turn was *both* slower (13.0s vs 10.7s on the first
	// corpus) and larger (134402), and the old accumulate-then-prune is
	// the 1.4x-slower baseline above.
	mergeState := func(to int, newCost int, newQueue [numRecentOffsets]int32, edge dpEdge) {
		s := states[to]
		worst := -1
		for idx := range s {
			if s[idx].queue == newQueue {
				if newCost < s[idx].cost {
					s[idx].cost = newCost
					s[idx].edge = edge
				}
				return
			}
			if worst < 0 || s[idx].cost > s[worst].cost {
				worst = idx
			}
		}
		if len(s) < beamWidth {
			states[to] = append(s, dpState{cost: newCost, queue: newQueue, edge: edge})
			return
		}
		if newCost < s[worst].cost {
			s[worst] = dpState{cost: newCost, queue: newQueue, edge: edge}
		}
	}

	literalCost := func(b byte) int {
		if model.mainLens != nil {
			if c := int(model.mainLens[b]); c > 0 {
				return c
			}
		}
		return flatLiteralBits
	}

	for i := 0; i < n; i++ {
		cur := states[i]
		if len(cur) == 0 {
			// Unreachable: cannot happen in practice since every position
			// is reachable via a chain of literal edges from 0, but guard
			// defensively rather than index an empty slice below.
			bstInsert(i)
			continue
		}

		freshCands := bstSearchAll(i)
		litCost := literalCost(data[i])

		// hash2 candidate (native, see matcher.go's hash2Candidate/
		// buildHash2PrevOcc doc): a length-2 fresh match, offered as one
		// more DP edge alongside the length>=3 freshCands above, rather
		// than an ex-post splice against an already-fixed table. Its
		// (offset, slot) don't depend on which beam state is relaxing
		// from position i, so this is computed once per position, not
		// once per state.
		hash2Offset, hash2OK := -1, false
		var hash2Cost int
		if q := prevOcc2[i]; o.dpHash2 && q >= 0 {
			off2 := i - int(q)
			slot2 := offsetSlot(uint32(off2))
			if numChars+slot2*numLenHeaders < nMainSyms {
				hash2Offset, hash2OK = off2, true
				// Depends only on slot2/off2, not on which beam state
				// is relaxing from position i, so compute it once per
				// position rather than once per state below.
				hash2Cost = model.matchCost(slot2, minMatchLen) + model.offsetExtraCost(slot2, off2)
			}
		}

		// Per-candidate cost depends only on cand/model, not on which
		// beam state is relaxing from position i, so compute it once
		// per position (here) rather than once per (state, candidate)
		// pair (inside the per-state loop below) -- same values, same
		// mergeState call order, just computed earlier.
		freshCosts := make([]int, len(freshCands))
		for fi, cand := range freshCands {
			slot := offsetSlot(uint32(cand.offset))
			freshCosts[fi] = model.matchCost(slot, cand.length) + model.offsetExtraCost(slot, cand.offset)
		}

		for si, s := range cur {
			mergeState(i+1, s.cost+litCost, s.queue, dpEdge{literal: data[i], repeat: -1, from: i, fromState: si})

			for k := 0; k < numRecentOffsets; k++ {
				off := s.queue[k]
				j := i - int(off)
				if j < 0 {
					continue
				}
				l, _ := matchLenCapped(j, i)
				if l < minMatchLen {
					continue
				}
				// Inline of the former repeatLengthSamples(l) helper:
				// the full length found, plus a midpoint shorter
				// length (letting the DP "under-shoot" a long repeat
				// run if a different choice afterward is cheaper),
				// rather than every length from minMatchLen up to the
				// full length -- trying every length is what a
				// from-scratch optimal parser would do, but is
				// unbounded cost for highly repetitive input. How many
				// samples to take is Options.DPRepeatLengthSamples
				// (options.go): at 1 only the full length is tried,
				// halving the repeat-edge count. Written as a fixed
				// [2]int + count instead of returning a []int so this
				// doesn't heap-allocate on every (state, recent-offset)
				// pair -- up to beamWidth*numRecentOffsets times per
				// position.
				var lens [2]int
				lens[0] = l
				numLens := 1
				if l > minMatchLen+4 && o.repeatLengthSamples >= 2 {
					lens[1] = l / 2
					numLens = 2
				}
				for li := 0; li < numLens; li++ {
					length := lens[li]
					if length < minMatchLen {
						continue
					}
					c := model.matchCost(k, length)
					nq := applyMatch(s.queue, k, int(off))
					mergeState(i+length, s.cost+c, nq, dpEdge{isMatch: true, offset: int(off), length: length, repeat: k, from: i, fromState: si})
				}
			}

			for fi, cand := range freshCands {
				c := freshCosts[fi]
				nq := applyMatch(s.queue, -1, cand.offset)
				mergeState(i+cand.length, s.cost+c, nq, dpEdge{isMatch: true, offset: cand.offset, length: cand.length, repeat: -1, from: i, fromState: si})
			}

			if hash2OK {
				nq2 := applyMatch(s.queue, -1, hash2Offset)
				mergeState(i+minMatchLen, s.cost+hash2Cost, nq2, dpEdge{isMatch: true, offset: hash2Offset, length: minMatchLen, repeat: -1, from: i, fromState: si})
			}
		}

		bstInsert(i)
	}

	finalStates := states[n]
	bestIdx := 0
	for idx := 1; idx < len(finalStates); idx++ {
		if finalStates[idx].cost < finalStates[bestIdx].cost {
			bestIdx = idx
		}
	}

	// pos strictly decreases by at least 1 each iteration (see edge
	// construction above), so the traversal takes at most n steps.
	toks := make([]token, 0, n)
	pos, si := n, bestIdx
	for pos > 0 {
		e := states[pos][si].edge
		if e.isMatch {
			toks = append(toks, token{isMatch: true, offset: e.offset, length: e.length, repeat: e.repeat})
		} else {
			toks = append(toks, token{literal: e.literal, repeat: -1})
		}
		pos, si = e.from, e.fromState
	}
	for l, r := 0, len(toks)-1; l < r; l, r = l+1, r-1 {
		toks[l], toks[r] = toks[r], toks[l]
	}
	return toks
}

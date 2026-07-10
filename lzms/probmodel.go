package lzms

// This file implements LZMS's adaptive binary probability model, ported
// from lzms_update_probability_entry / lzms_get_probability /
// lzms_init_probabilities in wimlib's include/wimlib/lzms_common.h and
// src/lzms_common.c (see lzms.go for the exact source commit).
//
// Each probability entry tracks the most recent probabilityDenominator
// (64) bits that were coded through it, ordered so that the most recently
// coded bit is the low-order bit of recentBits, plus a running count of
// how many of those 64 bits were zero. The chance that the *next* bit
// coded through this entry is 0 is defined as numRecentZeroBits out of 64,
// except that the degenerate values 0/64 and 64/64 are never used as-is
// (they are nudged to 1/64 and 63/64 respectively) since a range coder
// cannot represent a 0% or 100% probability.

const (
	probabilityBits        = 6
	probabilityDenominator = 1 << probabilityBits // 64

	initialProbability = 48
	initialRecentBits  = 0x0000000055555555

	numMainProbs     = 16
	numMatchProbs    = 32
	numLZProbs       = 64
	numLZRepProbs    = 64
	numDeltaProbs    = 64
	numDeltaRepProbs = 64

	numLZReps            = 3
	numDeltaReps         = 3
	numLZRepDecisions    = numLZReps - 1
	numDeltaRepDecisions = numDeltaReps - 1
)

// probEntry is one adaptive binary probability estimator.
type probEntry struct {
	numRecentZeroBits uint32
	recentBits        uint64
}

func newProbEntry() probEntry {
	return probEntry{
		numRecentZeroBits: initialProbability,
		recentBits:        initialRecentBits,
	}
}

// probability returns the chance, out of probabilityDenominator, that the
// next bit coded through this entry will be a 0.
func (e *probEntry) probability() uint32 {
	prob := e.numRecentZeroBits
	// if prob == 0 { prob++ }
	prob += uint32(int32(prob-1)) >> 31
	// if prob == probabilityDenominator { prob-- }
	prob -= prob >> probabilityBits
	return prob
}

// update folds a newly coded bit into the entry's adaptive state.
func (e *probEntry) update(bit int) {
	deltaZeroBits := int32(e.recentBits>>(probabilityDenominator-1)) - int32(bit)
	e.numRecentZeroBits = uint32(int32(e.numRecentZeroBits) + deltaZeroBits)
	e.recentBits = (e.recentBits << 1) | uint64(bit)
}

// probabilities bundles every probability table used to disambiguate LZMS
// item types, mirroring struct lzms_probabilites.
type probabilities struct {
	main     [numMainProbs]probEntry
	match    [numMatchProbs]probEntry
	lz       [numLZProbs]probEntry
	delta    [numDeltaProbs]probEntry
	lzRep    [numLZRepDecisions][numLZRepProbs]probEntry
	deltaRep [numDeltaRepDecisions][numDeltaRepProbs]probEntry
}

func newProbabilities() *probabilities {
	p := &probabilities{}
	for i := range p.main {
		p.main[i] = newProbEntry()
	}
	for i := range p.match {
		p.match[i] = newProbEntry()
	}
	for i := range p.lz {
		p.lz[i] = newProbEntry()
	}
	for i := range p.delta {
		p.delta[i] = newProbEntry()
	}
	for d := range p.lzRep {
		for i := range p.lzRep[d] {
			p.lzRep[d][i] = newProbEntry()
		}
	}
	for d := range p.deltaRep {
		for i := range p.deltaRep[d] {
			p.deltaRep[d][i] = newProbEntry()
		}
	}
	return p
}

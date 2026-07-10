package lzms

// This file implements a simple, correctness-focused LZMS encoder.
//
// Per this package's documented scope (see lzms.go), the encoder does not
// attempt to match wimlib's/Microsoft's compression ratio or bitstream. It
// uses only literals and explicit-offset LZ matches -- never delta matches
// and never repeat-offset ("rep") matches -- which are the two most
// elaborate parts of the format to get exactly right on the encode side
// (delta matches require a delta match finder; rep matches require
// replicating wimlib's one-item-delayed LRU queue update rule while
// choosing which rep index to emit). The decoder in decode.go fully
// implements both, since it must handle real wimlib/Microsoft-produced
// data, but this encoder deliberately sticks to the simplest valid subset
// of the format. See lzms.go's package doc and README.md for more detail
// on this tradeoff.
//
// Item encoding order (main bit, then match-type sub-decisions, then
// offset, then length) mirrors lzms_encode_item() in wimlib's
// src/lzms_compress.c (see lzms.go for the exact source commit); this
// encoder simply never takes the delta or rep branches.

const (
	minEncodeMatchLength = 2
	maxEncodeMatchLength = 1 << 20 // far below LZMS_MAX_MATCH_LENGTH; plenty for a simple encoder
	hashBits             = 17
	hashSize             = 1 << hashBits
	hashMinBytes         = 4
	maxChainLen          = 64
)

func hash4(b []byte) uint32 {
	v := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	return (v * 2654435761) >> (32 - hashBits)
}

func compress(data []byte) []byte {
	// The x86 translation filter is applied unconditionally by the
	// format (the decoder always undoes it), so the encoder must apply
	// it before match-finding, exactly mirroring lzms_x86_filter() being
	// called (undo == false) prior to compression in wimlib.
	buf := make([]byte, len(data))
	copy(buf, data)
	x86Filter(buf, false)

	n := len(buf)

	rc := newRangeEncoder()
	os := newOutputBitstream()
	probs := newProbabilities()

	numOffsetSlots := getNumOffsetSlots(n)
	literalCode := newHuffmanCode(numLiteralSyms, litCodeRebuildFreq)
	lzOffsetCode := newHuffmanCode(numOffsetSlots, lzOffCodeRebuildFreq)
	lengthCode := newHuffmanCode(numLengthSyms, lenCodeRebuildFreq)

	var mainState, matchState, lzState uint32

	// A simple hash-chain match finder over 4-byte prefixes.
	head := make([]int32, hashSize)
	for i := range head {
		head[i] = -1
	}
	prevChain := make([]int32, n)

	insert := func(pos int) {
		if pos+hashMinBytes > n {
			return
		}
		h := hash4(buf[pos:])
		prevChain[pos] = head[h]
		head[h] = int32(pos)
	}

	findMatch := func(pos int) (bestLen int, bestOff int) {
		if pos+hashMinBytes > n {
			return 0, 0
		}
		h := hash4(buf[pos:])
		cand := head[h]
		chainLen := 0
		maxLen := n - pos
		if maxLen > maxEncodeMatchLength {
			maxLen = maxEncodeMatchLength
		}
		for cand >= 0 && chainLen < maxChainLen {
			c := int(cand)
			// Compute match length at c vs pos.
			l := 0
			for l < maxLen && buf[c+l] == buf[pos+l] {
				l++
			}
			if l > bestLen {
				bestLen = l
				bestOff = pos - c
			}
			cand = prevChain[c]
			chainLen++
		}
		return bestLen, bestOff
	}

	encodeLiteral := func(b byte) {
		rc.encodeBit(0, &mainState, numMainProbs, probs.main[:])
		literalCode.encodeSymbol(os, int(b))
	}

	encodeMatch := func(offset, length int) {
		rc.encodeBit(1, &mainState, numMainProbs, probs.main[:])
		rc.encodeBit(0, &matchState, numMatchProbs, probs.match[:]) // LZ match
		rc.encodeBit(0, &lzState, numLZProbs, probs.lz[:])          // explicit offset

		slot := getOffsetSlot(uint32(offset))
		lzOffsetCode.encodeSymbol(os, slot)
		extra := extraOffsetBits[slot]
		if extra != 0 {
			os.writeBits(uint32(offset)-offsetSlotBase[slot], uint(extra))
		}

		lslot := getLengthSlot(uint32(length))
		lengthCode.encodeSymbol(os, lslot)
		lextra := extraLengthBits[lslot]
		if lextra != 0 {
			os.writeBits(uint32(length)-lengthSlotBase[lslot], uint(lextra))
		}
	}

	pos := 0
	for pos < n {
		bestLen, bestOff := findMatch(pos)
		if bestLen >= minEncodeMatchLength {
			// Simple lazy matching: see if starting one byte later
			// yields a strictly longer match; if so, emit a
			// literal now and let the next iteration take the
			// better match.
			if pos+1 < n {
				nextLen, _ := findMatch(pos + 1)
				if nextLen > bestLen {
					encodeLiteral(buf[pos])
					insert(pos)
					pos++
					continue
				}
			}
			encodeMatch(bestOff, bestLen)
			for i := 0; i < bestLen; i++ {
				insert(pos + i)
			}
			pos += bestLen
		} else {
			encodeLiteral(buf[pos])
			insert(pos)
			pos++
		}
	}

	rc.flush()
	os.flush()

	out := make([]byte, 0, len(rc.out)+len(os.bytes()))
	out = append(out, rc.out...)
	out = append(out, os.bytes()...)
	return out
}

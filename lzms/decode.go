package lzms

import "fmt"

// This file implements the main LZMS decompression loop, a direct port of
// lzms_decompress() in wimlib's src/lzms_decompress.c (see lzms.go for the
// exact source commit): item-type disambiguation via the range coder,
// LZ/delta match decoding (including the one-item-delayed repeat-offset
// LRU queues), and the final x86 filter undo pass.

const (
	litCodeRebuildFreq   = 1024 // LZMS_LITERAL_CODE_REBUILD_FREQ
	lzOffCodeRebuildFreq = 1024 // LZMS_LZ_OFFSET_CODE_REBUILD_FREQ
	lenCodeRebuildFreq   = 512  // LZMS_LENGTH_CODE_REBUILD_FREQ
	deltaOffRebuildFreq  = 1024 // LZMS_DELTA_OFFSET_CODE_REBUILD_FREQ
	deltaPowRebuildFreq  = 512  // LZMS_DELTA_POWER_CODE_REBUILD_FREQ

	numDeltaPowerSyms = 8 // LZMS_NUM_DELTA_POWER_SYMS
	numLiteralSyms    = 256
)

func decompressInto(out, in []byte) error {
	if len(in)%2 != 0 || len(in) < 4 {
		return fmt.Errorf("lzms: invalid compressed data length %d (must be even and >= 4)", len(in))
	}

	outNBytes := len(out)
	outPos := 0

	rd := newRangeDecoder(in)
	is := newInputBitstream(in)

	probs := newProbabilities()

	numOffsetSlots := getNumOffsetSlots(outNBytes)
	literalCode := newHuffmanCode(numLiteralSyms, litCodeRebuildFreq)
	lzOffsetCode := newHuffmanCode(numOffsetSlots, lzOffCodeRebuildFreq)
	lengthCode := newHuffmanCode(numLengthSyms, lenCodeRebuildFreq)
	deltaOffsetCode := newHuffmanCode(numOffsetSlots, deltaOffRebuildFreq)
	deltaPowerCode := newHuffmanCode(numDeltaPowerSyms, deltaPowRebuildFreq)

	// LRU queues for match sources. Index 3 is the "overflow" slot used
	// only transiently during the shift, mirroring wimlib's arrays of
	// size numLZReps+1 / numDeltaReps+1.
	var recentLZOffsets [numLZReps + 1]uint32
	for i := range recentLZOffsets {
		recentLZOffsets[i] = uint32(i + 1)
	}
	type deltaPair struct {
		power     uint32
		rawOffset uint32
	}
	var recentDeltaPairs [numDeltaReps + 1]deltaPair
	for i := range recentDeltaPairs {
		recentDeltaPairs[i] = deltaPair{0, uint32(i + 1)}
	}

	// prevItemType: 0 = literal, 1 = LZ match, 2 = delta match.
	prevItemType := 0

	var mainState, matchState, lzState, deltaState uint32
	var lzRepStates [numLZRepDecisions]uint32
	var deltaRepStates [numDeltaRepDecisions]uint32

	decodeLength := func() (uint32, error) {
		slot := lengthCode.decodeSymbol(is)
		length := lengthSlotBase[slot]
		numExtra := extraLengthBits[slot]
		if numExtra != 0 {
			length += is.readBits(uint(numExtra))
		}
		return length, nil
	}
	decodeLZOffset := func() uint32 {
		slot := lzOffsetCode.decodeSymbol(is)
		return offsetSlotBase[slot] + is.readBits(uint(extraOffsetBits[slot]))
	}
	decodeDeltaOffset := func() uint32 {
		slot := deltaOffsetCode.decodeSymbol(is)
		return offsetSlotBase[slot] + is.readBits(uint(extraOffsetBits[slot]))
	}

	for outPos != outNBytes {
		if rd.decodeBit(&mainState, numMainProbs, probs.main[:]) == 0 {
			// Literal.
			sym := literalCode.decodeSymbol(is)
			out[outPos] = byte(sym)
			outPos++
			prevItemType = 0
		} else if rd.decodeBit(&matchState, numMatchProbs, probs.match[:]) == 0 {
			// LZ match.
			var offset uint32

			if rd.decodeBit(&lzState, numLZProbs, probs.lz[:]) == 0 {
				// Explicit offset.
				offset = decodeLZOffset()
				recentLZOffsets[3] = recentLZOffsets[2]
				recentLZOffsets[2] = recentLZOffsets[1]
				recentLZOffsets[1] = recentLZOffsets[0]
			} else {
				// Repeat offset.
				delay := uint32(0)
				if prevItemType == 1 {
					delay = 1
				}
				if rd.decodeBit(&lzRepStates[0], numLZRepProbs, probs.lzRep[0][:]) == 0 {
					offset = recentLZOffsets[0+delay]
					recentLZOffsets[0+delay] = recentLZOffsets[0]
				} else if rd.decodeBit(&lzRepStates[1], numLZRepProbs, probs.lzRep[1][:]) == 0 {
					offset = recentLZOffsets[1+delay]
					recentLZOffsets[1+delay] = recentLZOffsets[1]
					recentLZOffsets[1] = recentLZOffsets[0]
				} else {
					offset = recentLZOffsets[2+delay]
					recentLZOffsets[2+delay] = recentLZOffsets[2]
					recentLZOffsets[2] = recentLZOffsets[1]
					recentLZOffsets[1] = recentLZOffsets[0]
				}
			}
			recentLZOffsets[0] = offset
			prevItemType = 1

			length, err := decodeLength()
			if err != nil {
				return err
			}

			if err := lzCopy(out, outPos, outNBytes, length, offset); err != nil {
				return err
			}
			outPos += int(length)
		} else {
			// Delta match.
			var power, rawOffset uint32

			if rd.decodeBit(&deltaState, numDeltaProbs, probs.delta[:]) == 0 {
				// Explicit offset.
				power = uint32(deltaPowerCode.decodeSymbol(is))
				rawOffset = decodeDeltaOffset()

				recentDeltaPairs[3] = recentDeltaPairs[2]
				recentDeltaPairs[2] = recentDeltaPairs[1]
				recentDeltaPairs[1] = recentDeltaPairs[0]
			} else {
				delay := uint32(0)
				if prevItemType == 2 {
					delay = 1
				}
				var pair deltaPair
				if rd.decodeBit(&deltaRepStates[0], numDeltaRepProbs, probs.deltaRep[0][:]) == 0 {
					pair = recentDeltaPairs[0+delay]
					recentDeltaPairs[0+delay] = recentDeltaPairs[0]
				} else if rd.decodeBit(&deltaRepStates[1], numDeltaRepProbs, probs.deltaRep[1][:]) == 0 {
					pair = recentDeltaPairs[1+delay]
					recentDeltaPairs[1+delay] = recentDeltaPairs[1]
					recentDeltaPairs[1] = recentDeltaPairs[0]
				} else {
					pair = recentDeltaPairs[2+delay]
					recentDeltaPairs[2+delay] = recentDeltaPairs[2]
					recentDeltaPairs[2] = recentDeltaPairs[1]
					recentDeltaPairs[1] = recentDeltaPairs[0]
				}
				power = pair.power
				rawOffset = pair.rawOffset
			}
			recentDeltaPairs[0] = deltaPair{power, rawOffset}
			prevItemType = 2

			length, err := decodeLength()
			if err != nil {
				return err
			}

			span := uint32(1) << power
			offset := rawOffset << power

			if offset>>power != rawOffset {
				return fmt.Errorf("lzms: delta offset overflow")
			}
			if offset+span < offset {
				return fmt.Errorf("lzms: delta offset+span overflow")
			}
			if uint64(offset)+uint64(span) > uint64(outPos) {
				return fmt.Errorf("lzms: delta match buffer underrun")
			}
			if uint64(length) > uint64(outNBytes-outPos) {
				return fmt.Errorf("lzms: delta match buffer overrun")
			}

			matchPos := outPos - int(offset)
			for l := uint32(0); l < length; l++ {
				out[outPos] = out[matchPos] + out[outPos-int(span)] - out[matchPos-int(span)]
				outPos++
				matchPos++
			}
		}
	}

	x86Filter(out, true)

	return nil
}

// lzCopy validates and performs a (possibly overlapping) LZ77-style copy
// of length bytes from offset bytes back in out[0:outPos] to out[outPos:],
// matching lz_copy()'s validation in wimlib's decompress_common.h (the
// fast-path SIMD-friendly copy is not relevant here; only the semantics of
// an overlap-correct byte-at-a-time copy and the bounds checks matter for
// correctness).
func lzCopy(out []byte, outPos, outNBytes int, length, offset uint32) error {
	if uint64(offset) > uint64(outPos) {
		return fmt.Errorf("lzms: match offset %d exceeds current position %d", offset, outPos)
	}
	if uint64(length) > uint64(outNBytes-outPos) {
		return fmt.Errorf("lzms: match length %d exceeds remaining output space", length)
	}
	src := outPos - int(offset)
	for i := uint32(0); i < length; i++ {
		out[outPos] = out[src]
		outPos++
		src++
	}
	return nil
}

package pa30

import "fmt"

// expandSlot7 reads the extra bits that follow a main-tree symbol whose
// slot number is 7, resolving it to the actual (large) slot number it
// stands in for, per the README's slot-7 special case:
//
//	0xx   -> slot = 43 + xx  (43..46)
//	10xxx -> slot = 47 + xx  (47..54)
//	11xxxx-> slot = 55 + xx  (55..70)
func expandSlot7(br *bitReader) (int, error) {
	b0, err := br.readBit()
	if err != nil {
		return 0, err
	}
	if b0 == 0 {
		v, err := br.readBits(2)
		if err != nil {
			return 0, err
		}
		return 43 + int(v), nil
	}
	b1, err := br.readBit()
	if err != nil {
		return 0, err
	}
	if b1 == 0 {
		v, err := br.readBits(3)
		if err != nil {
			return 0, err
		}
		return 47 + int(v), nil
	}
	v, err := br.readBits(4)
	if err != nil {
		return 0, err
	}
	return 55 + int(v), nil
}

// matchParams holds the slot-specific parameters decoded for one non-literal
// match. Exactly one of delta, lruIndex, offset is meaningful, depending on
// which slot range produced it (see decodeMatchParams).
//
// SRC/FULLSRC ADDRESSING -- REVERSE-ENGINEERED, NOT VERIFIED AGAINST REAL
// DATA. Per TODO.md's 2026-07-13 "SRC/FULLSRC decoding" research entry: the
// reference README/tool this package was otherwise clean-room-implemented
// from never actually decodes SRC/FULLSRC (its own `dump.c` only prints
// their length, never computing a source address), so there was no prose
// description to implement from. This package's SRC/FULLSRC handling
// instead comes from a background agent's static disassembly of the real
// `msdelta.dll`'s `ApplyDeltaB` (a genuine, documented Win32 API -- only its
// machine code was read, not any of its own source, since Microsoft ships
// no source for it; this is standard black-box/clean-room disassembly, not
// a licensing concern). That agent's finding: there is no persistent source
// cursor -- each match resolves `sourcePos = targetPos - distance` fresh,
// where `distance = delta` for SRC (slots 0-2) and `distance = 0` for
// FULLSRC (slot 3), and the "rift table" that would otherwise perturb this
// is confirmed (via embedded pipeline-description strings referencing
// `AddRiftEntry(emptyTable, sourceSize, 0)`) to be an identity/no-op for
// RAW (manifest) content. Numerically this makes `distance` interchangeable
// with the `offset` slots 4+ already use (see decodeContent), which is why
// slot 0-3 handling below reuses the same field.
//
// Two specific pieces of this are flagged by the disassembling agent itself
// as NOT fully confirmed, and remain open TODO items pending a real sample
// that actually exercises them (none sampled so far need SRC/FULLSRC at
// all):
//
//  1. Slot 2's bias (18-bit delta field): the disassembly showed an
//     UNCONDITIONAL `+0xa000`, not the signed-conditional ±0xa000 this
//     package previously had (a discrepancy against the DEFLATE-adjacent
//     assumption that slots 0/1's conditional-sign pattern would extend to
//     slot 2 too). Implemented below per the disassembly finding, but
//     unverified against any real slot-2 sample.
//  2. Whether SRC/FULLSRC matches update the DST/LRU repeat-offset queue:
//     the agent traced both the SRC and FULLSRC dispatch arms landing in
//     the same LRU-update code DST matches use, contradicting this
//     package's earlier assumption that only DST/LRU-repeat matches touch
//     the queue. Implemented below (decodeContent) per that finding --
//     "medium-high confidence, not exhaustively proven" per the agent --
//     but likewise unverified against real data.
//
// FULLSRC in particular is suspicious on its face: distance=0 means
// sourcePos == targetPos, i.e. a self-referential zero-offset match, which
// decodeContent's existing offset>0 validity check will reject outright
// (see its "invalid back-reference offset" error). Rather than silently
// papering over that with an unverified reinterpretation, this package
// deliberately lets that check surface it as an error -- a real FULLSRC-
// using sample is needed to determine what's actually being missed.
type matchParams struct {
	delta    int // slots 0-3 (SRC/FULLSRC): distance, per the doc above
	lruIndex int // slots 4-6 (LRU repeat)
	offset   int // slots 8+ (DST)
}

// decodeMatchParams reads the slot-specific bits following a main-tree
// match symbol, for the already-resolved slot (post expandSlot7 if slot==7
// was seen). Slot 3 (FULLSRC) has no extra bitstream parameters -- its
// distance is always 0 (see matchParams' doc comment for provenance and
// caveats on this whole slot 0-3 range).
func decodeMatchParams(br *bitReader, aligned *huffmanTree, slot int) (matchParams, error) {
	switch {
	case slot == 0:
		v, err := br.readBits(14)
		if err != nil {
			return matchParams{}, err
		}
		return matchParams{delta: int(v) - 0x2000}, nil
	case slot == 1:
		v, err := br.readBits(16)
		if err != nil {
			return matchParams{}, err
		}
		raw := int(v) - 0x8000
		if raw < 0 {
			return matchParams{delta: raw - 0x2000}, nil
		}
		return matchParams{delta: raw + 0x2000}, nil
	case slot == 2:
		v, err := br.readBits(18)
		if err != nil {
			return matchParams{}, err
		}
		raw := int(v) - 0x20000
		// UNVERIFIED (see matchParams' doc comment, point 1): disassembly of
		// the real msdelta.dll showed this bias applied unconditionally,
		// unlike slots 0/1's signed-conditional ±bias.
		return matchParams{delta: raw + 0xa000}, nil
	case slot == 3:
		return matchParams{delta: 0}, nil // FULLSRC: no parameters, distance always 0
	case slot >= 4 && slot <= 6:
		return matchParams{lruIndex: slot - 4}, nil
	case slot >= 8 && slot <= 10:
		return matchParams{offset: slot - 7}, nil
	case slot >= 11:
		topBits := 2 | ((slot - 11) & 1)
		verbLen := ((slot - 11) >> 1) + 1
		if verbLen < 4 {
			v, err := br.readBits(verbLen)
			if err != nil {
				return matchParams{}, err
			}
			return matchParams{offset: (topBits << uint(verbLen)) | int(v)}, nil
		}
		var hi uint32
		if verbLen > 4 {
			v, err := br.readBits(verbLen - 4)
			if err != nil {
				return matchParams{}, err
			}
			hi = v
		}
		alignedBits, err := aligned.decode(br)
		if err != nil {
			return matchParams{}, err
		}
		return matchParams{offset: (topBits << uint(verbLen)) | (int(hi) << 4) | alignedBits}, nil
	default:
		return matchParams{}, fmt.Errorf("pa30: unhandled slot %d", slot)
	}
}

// decodeLength decodes a match length given the main-tree symbol's 3-bit
// length subfield (lenField): a nonzero field directly encodes length-1
// (i.e. actual length = lenField+1); a zero field means the real length
// follows via the length tree (and possibly a further long-length field).
func decodeLength(br *bitReader, lengthTree *huffmanTree, lenField int) (int, error) {
	if lenField != 0 {
		return lenField + 1, nil
	}
	lengthBits, err := lengthTree.decode(br)
	if err != nil {
		return 0, err
	}
	if lengthBits > 0 {
		return lengthBits + 8, nil
	}
	long, err := readLongLength(br)
	if err != nil {
		return 0, err
	}
	return long + 8, nil
}

// readLongLength decodes the variable-length (8-32 bit) "long_length" field
// that follows a zero length-tree symbol: a run of zero bits (capped at 24)
// terminated by a 1 bit, then n = run-length+8 further bits, combined as
// (1<<n) | those bits.
func readLongLength(br *bitReader) (int, error) {
	zeros := 0
	for {
		bit, err := br.readBit()
		if err != nil {
			return 0, err
		}
		if bit == 1 {
			break
		}
		zeros++
		if zeros >= 24 {
			return 0, fmt.Errorf("pa30: long-length zero-run exceeds 24 bits")
		}
	}
	n := zeros + 8
	v, err := br.readBits(n)
	if err != nil {
		return 0, err
	}
	return (1 << uint(n)) | int(v), nil
}

// updateLRU applies val (a just-used DST/LRU back-reference offset) to the
// 3-element MRU-front LRU queue, deduplicating rather than allowing the
// same offset to appear twice. lru is initialized to {0,0,0} (unlike
// classic LZX Delta's {1,1,1}) and is only updated after DST/LRU matches,
// never after literal, SRC, or FULLSRC matches.
func updateLRU(lru *[3]int, val int) {
	if lru[0] != val {
		if lru[1] != val {
			lru[2] = lru[1]
		}
		lru[1] = lru[0]
	}
	lru[0] = val
}

// copyMatch appends length bytes to out, each copied from offset bytes
// before the current end of out. This is a standard LZ77-style copy: since
// bytes are appended one at a time and offset can be less than length, it
// correctly handles overlapping (run-length-style) copies.
func copyMatch(out *[]byte, offset, length int) {
	start := len(*out) - offset
	for i := 0; i < length; i++ {
		*out = append(*out, (*out)[start+i])
	}
}

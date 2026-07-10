package lzms

// This file implements the offset/length "slot" lookup helpers ported from
// lzms_get_slot()/lzms_get_offset_slot()/lzms_get_length_slot()/
// lzms_get_num_offset_slots() in wimlib's src/lzms_common.c and
// include/wimlib/lzms_common.h (see lzms.go for the exact source commit).
//
// LZMS encodes offsets and lengths as a Huffman-coded "slot" symbol (a
// coarse bucket) plus a fixed number of verbatim extra bits that refine the
// value within that bucket. offsetSlotBase/lengthSlotBase give the base
// (smallest) value represented by each slot, and extraOffsetBits/
// extraLengthBits give how many extra bits follow.

const (
	maxNumOffsetSyms = 799 // LZMS_MAX_NUM_OFFSET_SYMS
	numLengthSyms    = 54  // LZMS_NUM_LENGTH_SYMS

	maxMatchLength = 1073809578 // LZMS_MAX_MATCH_LENGTH
	maxMatchOffset = 1180427428 // LZMS_MAX_MATCH_OFFSET
	minMatchLength = 1          // LZMS_MIN_MATCH_LENGTH
)

// getSlot performs the same binary search as lzms_get_slot(): find the
// slot such that slotBaseTab[slot] <= value < slotBaseTab[slot+1].
func getSlot(value uint32, slotBaseTab []uint32, numSlots int) int {
	l, r := 0, numSlots-1
	for {
		slot := (l + r) / 2
		if value >= slotBaseTab[slot] {
			if value < slotBaseTab[slot+1] {
				return slot
			}
			l = slot + 1
		} else {
			r = slot - 1
		}
	}
}

func getOffsetSlot(offset uint32) int {
	return getSlot(offset, offsetSlotBase[:], maxNumOffsetSyms)
}

func getLengthSlot(length uint32) int {
	return getSlot(length, lengthSlotBase[:], numLengthSyms)
}

// getNumOffsetSlots returns the number of offset slots needed to represent
// all offsets that could occur within a buffer of the given uncompressed
// size, matching lzms_get_num_offset_slots().
func getNumOffsetSlots(uncompressedSize int) int {
	if uncompressedSize < 2 {
		return 0
	}
	return 1 + getOffsetSlot(uint32(uncompressedSize-1))
}

package lzms

// This file implements LZMS's x86 call/jump address translation filter,
// ported from lzms_x86_filter() (and its helpers find_next_opcode_default()
// and translate_if_needed()) in wimlib's src/lzms_common.c (see lzms.go for
// the exact source commit). This filter is applied unconditionally to the
// decompressed buffer as a postprocessing step (decompression) or to the
// input buffer before LZ/delta matching (compression); its purpose is to
// convert relative x86 branch/call targets into absolute addresses (which
// tend to repeat and thus compress better), then undo that translation
// after decompression.
//
// This is a direct translation of wimlib's portable (non-SSE4.2) code path;
// the SSE4.2-accelerated opcode scanner in wimlib is purely a performance
// optimization equivalent to the default path and is not reproduced here.

const (
	x86IDWindowSize         = 65535 // LZMS_X86_ID_WINDOW_SIZE
	x86MaxTranslationOffset = 1023  // LZMS_X86_MAX_TRANSLATION_OFFSET
)

var isPotentialOpcode [256]bool

func init() {
	for _, b := range []byte{0x48, 0x4C, 0xE8, 0xE9, 0xF0, 0xFF} {
		isPotentialOpcode[b] = true
	}
}

func getLE16(b []byte, off int) uint16 {
	return uint16(b[off]) | uint16(b[off+1])<<8
}

func putLE16(b []byte, off int, v uint16) {
	b[off] = byte(v)
	b[off+1] = byte(v >> 8)
}

func getLE32(b []byte, off int) uint32 {
	return uint32(b[off]) | uint32(b[off+1])<<8 | uint32(b[off+2])<<16 | uint32(b[off+3])<<24
}

func putLE32(b []byte, off int, v uint32) {
	b[off] = byte(v)
	b[off+1] = byte(v >> 8)
	b[off+2] = byte(v >> 16)
	b[off+3] = byte(v >> 24)
}

// x86Filter translates relative addresses embedded in x86 instructions
// into absolute addresses (undo == false), or undoes this translation
// (undo == true). It is a direct port of lzms_x86_filter().
func x86Filter(data []byte, undo bool) {
	size := len(data)
	if size <= 17 {
		return
	}

	lastTargetUsages := make([]int32, 65536)
	for i := range lastTargetUsages {
		lastTargetUsages[i] = -int32(x86IDWindowSize) - 1
	}

	lastX86Pos := -int32(x86MaxTranslationOffset) - 1

	// p starts at data[1]; the very first byte must be ignored completely.
	p := 1
	tailPos := size - 16

	for {
		p = findNextOpcodeDefault(data, p, tailPos)
		if p >= tailPos {
			break
		}
		p = translateIfNeeded(data, p, &lastX86Pos, lastTargetUsages, undo)
	}
}

func findNextOpcodeDefault(data []byte, p, tailPos int) int {
	for {
		if p >= tailPos {
			return p
		}
		if isPotentialOpcode[data[p]] {
			return p
		}
		p++
	}
}

func translateIfNeeded(data []byte, p int, lastX86Pos *int32, lastTargetUsages []int32, undo bool) int {
	maxTransOffset := int32(x86MaxTranslationOffset)

	var opcodeNBytes int

	switch {
	case data[p] >= 0xF0:
		if data[p]&0x0F != 0 {
			// 0xFF (instruction group)
			if p+1 < len(data) && data[p+1] == 0x15 {
				opcodeNBytes = 2
				goto haveOpcode
			}
		} else {
			// 0xF0 (lock prefix)
			if p+2 < len(data) && data[p+1] == 0x83 && data[p+2] == 0x05 {
				opcodeNBytes = 3
				goto haveOpcode
			}
		}
	case data[p] <= 0x4C:
		if p+2 < len(data) && (data[p+2]&0x07) == 0x05 {
			if data[p+1] == 0x8D ||
				(data[p+1] == 0x8B && (data[p]&0x04) == 0 && (data[p+2]&0xF0) == 0) {
				opcodeNBytes = 3
				goto haveOpcode
			}
		}
	default:
		if data[p]&0x01 != 0 {
			// 0xE9: Jump relative -- explicitly excluded.
			return p + 4 + 1
		}
		// 0xE8: Call relative.
		opcodeNBytes = 1
		maxTransOffset >>= 1
		goto haveOpcode
	}

	return p + 1

haveOpcode:
	i := int32(p)
	p += opcodeNBytes
	if p+4 > len(data) {
		// Not enough room for the 32-bit displacement; nothing to do.
		return p
	}
	var target16 uint16
	if undo {
		if i-*lastX86Pos <= maxTransOffset {
			n := getLE32(data, p)
			putLE32(data, p, n-uint32(i))
		}
		target16 = uint16(i) + getLE16(data, p)
	} else {
		target16 = uint16(i) + getLE16(data, p)
		if i-*lastX86Pos <= maxTransOffset {
			n := getLE32(data, p)
			putLE32(data, p, n+uint32(i))
		}
	}

	i += int32(opcodeNBytes) + 4 - 1

	if i-lastTargetUsages[target16] <= x86IDWindowSize {
		*lastX86Pos = i
	}
	lastTargetUsages[target16] = i

	return p + 4
}

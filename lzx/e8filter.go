package lzx

import "encoding/binary"

// This file implements the "E8" x86 CALL-instruction address-translation
// filter used unconditionally by WIM's flavor of LZX (see lzx.go's
// WIM-vs-CAB notes). Ported directly from the scalar (non-SIMD) reference
// implementation in wimlib's src/lzx_common.c (lzx_e8_filter,
// do_translate_target, undo_translate_target), which the file's own comment
// confirms is behaviorally identical to its vectorized fast paths -- this
// port deliberately keeps the same sentinel-byte trick (rather than a
// naive bounds-checked scan) so that its edge-of-buffer behavior around the
// last 6/10 bytes matches wimlib exactly, byte for byte.
func e8Filter(data []byte, translate func(data []byte, pos int, inputPos int32)) {
	size := len(data)
	if size <= 10 {
		return
	}

	tail := size - 6
	var saved [6]byte
	copy(saved[:], data[tail:tail+6])
	for i := tail; i < tail+6; i++ {
		data[i] = 0xE8
	}

	p := 0
	for {
		for data[p] != 0xE8 {
			p++
		}
		if p >= tail {
			break
		}
		translate(data, p+1, int32(p))
		p += 5
	}

	copy(data[tail:tail+6], saved[:])
}

func doTranslateTarget(data []byte, pos int, inputPos int32) {
	relOffset := int32(binary.LittleEndian.Uint32(data[pos : pos+4]))
	if relOffset >= -inputPos && relOffset < wimMagicFilesize {
		var absOffset int32
		if relOffset < wimMagicFilesize-inputPos {
			absOffset = relOffset + inputPos
		} else {
			absOffset = relOffset - wimMagicFilesize
		}
		binary.LittleEndian.PutUint32(data[pos:pos+4], uint32(absOffset))
	}
}

func undoTranslateTarget(data []byte, pos int, inputPos int32) {
	absOffset := int32(binary.LittleEndian.Uint32(data[pos : pos+4]))
	if absOffset >= 0 {
		if absOffset < wimMagicFilesize {
			relOffset := absOffset - inputPos
			binary.LittleEndian.PutUint32(data[pos:pos+4], uint32(relOffset))
		}
	} else if absOffset >= -inputPos {
		relOffset := absOffset + wimMagicFilesize
		binary.LittleEndian.PutUint32(data[pos:pos+4], uint32(relOffset))
	}
}

// lzxPreprocess applies the E8 filter before compression (relative ->
// absolute call targets).
func lzxPreprocess(data []byte) {
	e8Filter(data, doTranslateTarget)
}

// lzxPostprocess reverses the E8 filter after decompression (absolute ->
// relative call targets).
func lzxPostprocess(data []byte) {
	e8Filter(data, undoTranslateTarget)
}

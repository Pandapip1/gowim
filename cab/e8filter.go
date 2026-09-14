package cab

import "encoding/binary"

// e8Untranslate reverses the x86 CALL-instruction (0xE8) address
// translation LZX's encoder optionally applies before compression, ported
// line-for-line from libmspack's lzxd.c lzxd_decompress "does this intel
// block _really_ need decoding?" section. data is one frame's decompressed
// bytes (already copied out of the circular window so it can be mutated
// in place); curposStart is that frame's absolute offset within the whole
// decompressed stream (lzx->offset at the start of the frame); filesize is
// the translation size read from the LZX header's optional 32-bit field.
//
// Only the last 10 bytes of a frame are exempt (dataend = frame_size-10),
// per libmspack's own header comment correcting the original "cab-sdk.exe"
// LZX specification's pseudocode, which checked only the last 6 bytes -- a
// documented real discrepancy between the spec prose and the actual
// shipped implementation, not something to guess at.
func e8Untranslate(data []byte, curposStart int, filesize int32) {
	if len(data) <= 10 {
		return
	}
	dataend := len(data) - 10
	curpos := int32(curposStart)
	i := 0
	for i < dataend {
		b := data[i]
		i++
		if b != 0xE8 {
			curpos++
			continue
		}
		absOff := int32(binary.LittleEndian.Uint32(data[i : i+4]))
		if absOff >= -curpos && absOff < filesize {
			var relOff int32
			if absOff >= 0 {
				relOff = absOff - curpos
			} else {
				relOff = absOff + filesize
			}
			binary.LittleEndian.PutUint32(data[i:i+4], uint32(relOff))
		}
		i += 4
		curpos += 5
	}
}

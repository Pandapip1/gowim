package inf

import (
	"encoding/binary"
	"unicode/utf16"
)

// utf16BOM is the UTF-16LE byte order mark that, per the Windows Hardware
// documentation on Unicode INF files, must prefix a Unicode-encoded INF.
// Non-Unicode INF files (ANSI/OEM/UTF-8-without-BOM) have no such marker.
var utf16BOM = []byte{0xff, 0xfe}

var le = binary.LittleEndian

// decodeFile detects an INF file's encoding and returns its text content as
// a Go string, along with whether it was detected as Unicode (UTF-16LE with
// BOM).
//
// For a non-Unicode file, the returned string is the raw input bytes
// reinterpreted as a string with no transcoding: a []byte-to-string
// conversion copies bytes verbatim, so this is exact regardless of the
// file's actual (unspecified, and out of scope - see the package doc)
// single-byte or DBCS codepage. Only the ASCII punctuation significant to
// the INF grammar is ever inspected.
func decodeFile(data []byte) (text string, unicode bool) {
	if len(data) >= 2 && data[0] == utf16BOM[0] && data[1] == utf16BOM[1] {
		return utf16leToString(data[2:]), true
	}
	return string(data), false
}

// encodeFile serializes text back to bytes, prefixed with a UTF-16LE BOM and
// UTF-16LE-encoded when unicode is true, or as raw bytes (the inverse of the
// verbatim conversion in decodeFile) otherwise.
func encodeFile(text string, unicode bool) []byte {
	if !unicode {
		return []byte(text)
	}
	out := make([]byte, 0, len(utf16BOM)+len(text)*2)
	out = append(out, utf16BOM...)
	out = append(out, stringToUTF16LE(text)...)
	return out
}

// utf16leToString decodes a UTF-16LE byte slice (no BOM) into a Go string.
// An odd trailing byte is ignored.
func utf16leToString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = le.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u16))
}

// stringToUTF16LE encodes a Go string as UTF-16LE bytes (no BOM).
func stringToUTF16LE(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	out := make([]byte, len(u16)*2)
	for i, c := range u16 {
		le.PutUint16(out[i*2:], c)
	}
	return out
}

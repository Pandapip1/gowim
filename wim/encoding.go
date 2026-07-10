package wim

import (
	"unicode/utf16"
	"unicode/utf8"
)

// utf16leToString decodes a UTF-16LE byte slice (no BOM, no terminator) into a
// Go string. An odd trailing byte is ignored.
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

// stringToUTF16LE encodes a Go string as UTF-16LE bytes (no BOM, no
// terminator).
func stringToUTF16LE(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	out := make([]byte, len(u16)*2)
	for i, c := range u16 {
		le.PutUint16(out[i*2:], c)
	}
	return out
}

// validUTF8 reports whether s is valid UTF-8 (used to sanity-check decoded
// names before use in higher-level APIs).
func validUTF8(s string) bool { return utf8.ValidString(s) }

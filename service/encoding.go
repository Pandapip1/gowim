package service

import "unicode/utf16"

// stringToUTF16LE encodes a Go string as UTF-16LE bytes (no BOM, no
// terminator), matching the encoding regf.Key.Name/regf.Value.Name expect
// when their COMP_NAME flag is unset. regf's own helper of the same shape is
// unexported, so it is duplicated here - mirroring how cat/der.go duplicates
// it from wim/encoding.go, and driver/install.go duplicates it again for the
// same reason.
func stringToUTF16LE(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	out := make([]byte, len(u16)*2)
	for i, c := range u16 {
		out[i*2] = byte(c)
		out[i*2+1] = byte(c >> 8)
	}
	return out
}

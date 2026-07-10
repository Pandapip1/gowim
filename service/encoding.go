package service

import (
	"encoding/binary"
	"unicode/utf16"
)

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

// utf16LEToString decodes UTF-16LE bytes (no BOM, no terminator) back to a
// Go string - the reverse of stringToUTF16LE, used to decode REG_SZ/
// REG_EXPAND_SZ value Data when reading a service back out of the registry.
// A trailing odd byte (which should never occur for a well-formed UTF-16LE
// value) is ignored, mirroring how regf's own vk/multiSZ decoding is
// tolerant of malformed trailing bytes rather than panicking.
func utf16LEToString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u16))
}

// multiSZToStrings splits a REG_MULTI_SZ value's Data into its component
// UTF-16LE strings - the reverse of multiSZBytes. Both the inter-string and
// final terminators are a UTF-16 NUL (0x0000); an empty/absent Data decodes
// to nil, matching Service.DependOnGroup/DependOnService's "nil for none"
// convention.
func multiSZToStrings(b []byte) []string {
	var out []string
	start := 0
	for i := 0; i+1 < len(b); i += 2 {
		if b[i] == 0 && b[i+1] == 0 {
			if i > start {
				out = append(out, utf16LEToString(b[start:i]))
			}
			start = i + 2
		}
	}
	return out
}

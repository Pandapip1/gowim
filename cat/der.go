package cat

import (
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

// be is the byte order for BMPString content (UCS-2, big-endian), as used by
// ASN.1's universal BMPString type.
var be = binary.BigEndian

// bmpStringToUTF8 decodes BMPString content (UCS-2, big-endian, as produced
// by encoding/asn1's BMPString parser) into a Go string.
func bmpStringToUTF8(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = be.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u16))
}

// utf8ToBMPString encodes a Go string as BMPString content (UCS-2,
// big-endian).
func utf8ToBMPString(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	out := make([]byte, len(u16)*2)
	for i, c := range u16 {
		be.PutUint16(out[i*2:], c)
	}
	return out
}

// utf16leToString decodes a UTF-16LE byte slice (no BOM) into a Go string,
// trimming a trailing NUL if present (CAT_NAMEVALUE string values are
// conventionally NUL-terminated). An odd trailing byte is ignored.
func utf16leToString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return strings.TrimRight(string(utf16.Decode(u16)), "\x00")
}

// stringToUTF16LE encodes a Go string as UTF-16LE bytes (no BOM), appending a
// trailing NUL terminator to match the convention used by CAT_NAMEVALUE
// string values.
func stringToUTF16LE(s string) []byte {
	u16 := utf16.Encode([]rune(s + "\x00"))
	out := make([]byte, len(u16)*2)
	for i, c := range u16 {
		binary.LittleEndian.PutUint16(out[i*2:], c)
	}
	return out
}

package appx

import (
	"crypto/sha256"
	"encoding/binary"
	"unicode/utf16"
)

// crockfordAlphabet is the Douglas Crockford Base32 alphabet (excludes
// I/L/O/U to avoid confusion with 1/1/0/V), lowercased. Matches
// github.com/russellbanks/package-family-name's crockford::encode_lower
// (read directly, not guessed - see appx.go's doc comment).
const crockfordAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// crockfordEncode13 Crockford-Base32-encodes an 8-byte (64-bit) value into
// 13 lowercase characters (65 bits: the 64 input bits followed by one zero
// padding bit, split into 13 groups of 5 bits, most-significant-bit
// first). This is a direct, line-for-line port of
// github.com/russellbanks/package-family-name's crockford::encode_lower
// (src/crockford.rs, read 2026-07-14 from a local clone of the upstream
// repo at /tmp/package-family-name - see appx.go's doc comment for
// provenance), not an independent reimplementation from prose.
func crockfordEncode13(input [8]byte) [13]byte {
	var out [13]byte
	n := binary.BigEndian.Uint64(input[:])

	i := 0
	for shift := 59; shift >= 4; shift -= 5 {
		out[i] = crockfordAlphabet[(n>>uint(shift))&0x1F]
		i++
	}
	// Final character encodes the remaining 4 bits plus one zero padding bit.
	out[12] = crockfordAlphabet[(n<<1)&0x1F]
	return out
}

// PublisherID computes the 13-character Crockford Base32 "Publisher ID"
// from a package identity's Publisher string (its X.509 distinguished
// name, e.g. "CN=Microsoft Corporation, O=Microsoft Corporation, ..."):
// SHA-256 of the UTF-16LE-encoded Publisher string, Crockford Base32
// encoding of the hash's first 8 bytes (see crockfordEncode13). Matches
// github.com/russellbanks/package-family-name's PublisherId::new (read
// 2026-07-14, see appx.go's doc comment) and cross-checked against real
// data: PublisherID of Microsoft's well-known publisher string is
// "8wekyb3d8bbwe", matching every Microsoft-published package folder name
// observed in a real Windows 11 23H2 image.
func PublisherID(publisher string) string {
	u16 := utf16.Encode([]rune(publisher))
	buf := make([]byte, 0, len(u16)*2)
	for _, u := range u16 {
		buf = append(buf, byte(u), byte(u>>8))
	}
	sum := sha256.Sum256(buf)
	var first8 [8]byte
	copy(first8[:], sum[:8])
	enc := crockfordEncode13(first8)
	return string(enc[:])
}

// PackageFamilyName computes "<name>_<PublisherID>" (see PublisherID) from
// a package identity's Name and Publisher - the same value the real
// PackageFamilyNameFromId Win32 API returns, and what a provisioned
// package's AppxAllUserStore\Deprovisioned marker key is named after (see
// remove.go).
func PackageFamilyName(name, publisher string) string {
	return name + "_" + PublisherID(publisher)
}

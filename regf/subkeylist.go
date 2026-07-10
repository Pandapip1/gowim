package regf

import (
	"fmt"
	"unicode"
	"unicode/utf16"
)

// Subkey-list cell signatures, from the "Hive bin cell values" and "Sub key
// list" sections.
const (
	subkeyListLF = "lf" // hash leaf, undocumented truncated-name hash (see below)
	subkeyListLH = "lh" // hashed leaf, documented hash (see lhHash)
	subkeyListLI = "li" // index leaf, no hash
	subkeyListRI = "ri" // index root, entries point at further lf/lh/li lists
)

// subkeyListHeaderSize is the 4-byte {signature,element-count} header shared
// by all four version-1.2+ subkey-list shapes.
const subkeyListHeaderSize = 4

// subkeyListEntry is one decoded element of a subkey list: a cell offset,
// plus (for lf/lh lists) the raw stored hash. For li/ri lists Hash is
// unused.
type subkeyListEntry struct {
	Offset uint32
	Hash   uint32
}

// parsedSubkeyList is a subkey list cell, decoded but not yet resolved
// (ri entries still just point at another subkey-list cell offset; hive.go
// walks those).
type parsedSubkeyList struct {
	Signature string
	Entries   []subkeyListEntry
}

// parseSubkeyList decodes a subkey-list cell's data (not including trailing
// padding).
func parseSubkeyList(data []byte) (*parsedSubkeyList, error) {
	if len(data) < subkeyListHeaderSize {
		return nil, fmt.Errorf("subkey list cell too short: need %d bytes, have %d", subkeyListHeaderSize, len(data))
	}
	sig := string(data[0:2])
	count := int(le.Uint16(data[2:4]))
	l := &parsedSubkeyList{Signature: sig}

	switch sig {
	case subkeyListLF, subkeyListLH:
		const entrySize = 8
		if subkeyListHeaderSize+count*entrySize > len(data) {
			return nil, fmt.Errorf("%s subkey list: %d entries overrun cell", sig, count)
		}
		for i := 0; i < count; i++ {
			o := subkeyListHeaderSize + i*entrySize
			l.Entries = append(l.Entries, subkeyListEntry{
				Offset: le.Uint32(data[o : o+4]),
				Hash:   le.Uint32(data[o+4 : o+8]),
			})
		}
	case subkeyListLI, subkeyListRI:
		const entrySize = 4
		if subkeyListHeaderSize+count*entrySize > len(data) {
			return nil, fmt.Errorf("%s subkey list: %d entries overrun cell", sig, count)
		}
		for i := 0; i < count; i++ {
			o := subkeyListHeaderSize + i*entrySize
			l.Entries = append(l.Entries, subkeyListEntry{Offset: le.Uint32(data[o : o+4])})
		}
	default:
		return nil, fmt.Errorf("subkey list: unrecognized signature %q", sig)
	}
	return l, nil
}

// appendTo serializes the subkey list cell's data, without padding.
func (l *parsedSubkeyList) appendTo(dst []byte) ([]byte, error) {
	var hdr [subkeyListHeaderSize]byte
	copy(hdr[0:2], l.Signature)
	le.PutUint16(hdr[2:4], uint16(len(l.Entries)))
	dst = append(dst, hdr[:]...)
	switch l.Signature {
	case subkeyListLF, subkeyListLH:
		for _, e := range l.Entries {
			var buf [8]byte
			le.PutUint32(buf[0:4], e.Offset)
			le.PutUint32(buf[4:8], e.Hash)
			dst = append(dst, buf[:]...)
		}
	case subkeyListLI, subkeyListRI:
		for _, e := range l.Entries {
			var buf [4]byte
			le.PutUint32(buf[0:4], e.Offset)
			dst = append(dst, buf[:]...)
		}
	default:
		return dst, fmt.Errorf("subkey list: unrecognized signature %q", l.Signature)
	}
	return dst, nil
}

// lhHash computes the "lh" subkey-list hash of a key name, per the "LH sub
// key hash algorithm" section:
//
//	uint32_t hash_value = 0
//	for each character in the string (2 bytes at a time for UTF-16LE):
//	    hash_value *= 37
//	    hash_value += uppercase(character)
//
// nameIsASCII selects whether raw is interpreted as an 8-bit-per-character
// ASCII string or a UTF-16LE string (matching KeyFlagCompName); the
// uppercase operation is applied per the spec's note that "the uppercase
// function must be able to handle Unicode" -- this package uses
// unicode/utf16 plus Go's Unicode-aware upper-casing, which is the closest
// verifiable interpretation absent a Microsoft-published reference
// implementation. Note that the exact hash algorithm for "lf" lists is not
// specified anywhere in the source document (only lh's is), so this
// package never synthesizes lf lists of its own -- see the package's
// subkey-list allocation strategy in hive.go.
func lhHash(raw []byte, nameIsASCII bool) uint32 {
	var hash uint32
	if nameIsASCII {
		for _, c := range raw {
			hash *= 37
			hash += uint32(unicode.ToUpper(rune(c)))
		}
		return hash
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = le.Uint16(raw[i*2:])
	}
	for _, r := range utf16.Decode(units) {
		hash *= 37
		hash += uint32(unicode.ToUpper(r))
	}
	return hash
}

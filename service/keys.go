package service

import (
	"encoding/binary"
	"strings"

	"github.com/gavin-john/gowim/regf"
)

// FindSubkey returns the direct child of key named name (matching Windows'
// case-insensitive registry namespace), or nil if none exists.
func FindSubkey(key *regf.Key, name string) *regf.Key {
	for _, k := range key.Subkeys {
		if strings.EqualFold(k.NameUTF8(), name) {
			return k
		}
	}
	return nil
}

// FindOrCreateSubkey returns the direct child of key named name, creating it
// (as a plain, non-root, UTF-16LE-named key with no values/subkeys of its
// own yet) if it does not already exist. Calling it twice with the same name
// returns the same *regf.Key both times, which is what makes Install (and
// any other caller building up a *regf.Key tree, e.g. the sibling driver
// package's CriticalDeviceDatabase merge) safe to call repeatedly without
// duplicating subkeys.
func FindOrCreateSubkey(key *regf.Key, name string) *regf.Key {
	if child := FindSubkey(key, name); child != nil {
		return child
	}
	child := &regf.Key{Name: stringToUTF16LE(name)}
	key.Subkeys = append(key.Subkeys, child)
	return child
}

// FindValue returns a pointer to key's value named name (matching Windows'
// case-insensitive registry namespace), or nil if none exists.
func FindValue(key *regf.Key, name string) *regf.Value {
	for i := range key.Values {
		if strings.EqualFold(key.Values[i].NameUTF8(), name) {
			return &key.Values[i]
		}
	}
	return nil
}

// SetValue creates or overwrites key's value named name with the given type
// and data, so that calling it twice with the same name replaces rather than
// duplicates the value - the value-side half of what makes Install
// idempotent.
func SetValue(key *regf.Key, name string, typ uint32, data []byte) {
	if v := FindValue(key, name); v != nil {
		v.Type = typ
		v.Data = data
		return
	}
	key.Values = append(key.Values, regf.Value{Name: stringToUTF16LE(name), Type: typ, Data: data})
}

// RemoveValue deletes key's value named name (if present), keeping a
// re-installation of a registration whose optional fields changed (or were
// removed) from leaving stale values behind.
func RemoveValue(key *regf.Key, name string) {
	for i := range key.Values {
		if strings.EqualFold(key.Values[i].NameUTF8(), name) {
			key.Values = append(key.Values[:i], key.Values[i+1:]...)
			return
		}
	}
}

// uint32LEBytes little-endian-encodes v as a 4-byte REG_DWORD value.
func uint32LEBytes(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

// multiSZBytes encodes strs as a REG_MULTI_SZ value: each string as
// UTF-16LE followed by a UTF-16 NUL terminator, with the whole list
// terminated by one further (empty-string) NUL.
func multiSZBytes(strs []string) []byte {
	var out []byte
	for _, s := range strs {
		out = append(out, stringToUTF16LE(s)...)
		out = append(out, 0, 0)
	}
	out = append(out, 0, 0)
	return out
}

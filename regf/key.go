package regf

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Subkey returns the direct child of k named name (matching Windows'
// case-insensitive registry namespace), or nil if none exists.
func (k *Key) Subkey(name string) *Key {
	for _, c := range k.Subkeys {
		if strings.EqualFold(c.NameUTF8(), name) {
			return c
		}
	}
	return nil
}

// DeleteSubkey deletes k's direct child subkey named name (matching
// Windows' case-insensitive registry namespace) -- and, since Subkeys holds
// *Key pointers, implicitly its entire subtree -- reporting whether a
// subkey was actually removed.
func (k *Key) DeleteSubkey(name string) bool {
	for i, c := range k.Subkeys {
		if strings.EqualFold(c.NameUTF8(), name) {
			k.Subkeys = append(k.Subkeys[:i], k.Subkeys[i+1:]...)
			return true
		}
	}
	return false
}

// FindOrCreateSubkey returns the direct child of k named name, creating it
// (as a plain, non-root, UTF-16LE-named key with no values/subkeys of its
// own yet) if it does not already exist. Calling it twice with the same
// name returns the same *Key both times, which is what makes callers
// building up a tree (e.g. the sibling service/driver packages) safe to
// call repeatedly without duplicating subkeys.
func (k *Key) FindOrCreateSubkey(name string) *Key {
	if child := k.Subkey(name); child != nil {
		return child
	}
	child := &Key{Name: stringToUTF16LE(name)}
	k.Subkeys = append(k.Subkeys, child)
	return child
}

// Value returns a pointer to k's value named name (matching Windows'
// case-insensitive registry namespace), or nil if none exists.
func (k *Key) Value(name string) *Value {
	for i := range k.Values {
		if strings.EqualFold(k.Values[i].NameUTF8(), name) {
			return &k.Values[i]
		}
	}
	return nil
}

// SetValue creates or overwrites k's value named name with the given type
// and data, so that calling it twice with the same name replaces rather
// than duplicates the value.
func (k *Key) SetValue(name string, typ uint32, data []byte) {
	if v := k.Value(name); v != nil {
		v.Type = typ
		v.Data = data
		return
	}
	k.Values = append(k.Values, Value{Name: stringToUTF16LE(name), Type: typ, Data: data})
}

// DeleteValue deletes k's value named name (matching Windows'
// case-insensitive registry namespace), reporting whether a value was
// actually removed.
func (k *Key) DeleteValue(name string) bool {
	for i := range k.Values {
		if strings.EqualFold(k.Values[i].NameUTF8(), name) {
			k.Values = append(k.Values[:i], k.Values[i+1:]...)
			return true
		}
	}
	return false
}

// splitKeyPath splits a registry path into its non-empty components,
// accepting both '\'- and '/'-separated input (real registry paths are
// conventionally backslash-separated; this package also accepts '/' so
// callers don't have to normalize first, matching the sibling wim
// package's splitPath convention). Leading, trailing, and repeated
// separators are ignored, so "", "/", and "\" all yield an empty component
// list (referring to the key itself).
func splitKeyPath(p string) []string {
	p = strings.ReplaceAll(p, "\\", "/")
	var out []string
	for _, c := range strings.Split(p, "/") {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// OpenPath navigates from k through each backslash- (or slash-) separated
// component of path, returning the resulting Key, or nil if any component
// does not exist or names something that isn't a subkey. An empty path (or
// one consisting only of separators) returns k itself.
func (k *Key) OpenPath(path string) *Key {
	cur := k
	for _, name := range splitKeyPath(path) {
		cur = cur.Subkey(name)
		if cur == nil {
			return nil
		}
	}
	return cur
}

// FindOrCreatePath is OpenPath's create-if-missing counterpart: it creates
// every missing component along path (as plain empty keys, via
// FindOrCreateSubkey) and returns the final Key. An empty path (or one
// consisting only of separators) returns k itself.
func (k *Key) FindOrCreatePath(path string) *Key {
	cur := k
	for _, name := range splitKeyPath(path) {
		cur = cur.FindOrCreateSubkey(name)
	}
	return cur
}

// DeletePath deletes the subkey named by path's final component (and its
// entire subtree), reporting whether it was found. path's parent
// components must already exist and each be a subkey of the previous one;
// if any component through the parent is missing, or path is empty (there
// is no "final component" to delete), DeletePath reports false without
// modifying anything.
func (k *Key) DeletePath(path string) bool {
	components := splitKeyPath(path)
	if len(components) == 0 {
		return false
	}
	parent := k.OpenPath(strings.Join(components[:len(components)-1], "/"))
	if parent == nil {
		return false
	}
	return parent.DeleteSubkey(components[len(components)-1])
}

// DWORD decodes v's data as a little-endian REG_DWORD, erroring if it is
// not exactly 4 bytes.
func (v *Value) DWORD() (uint32, error) {
	if len(v.Data) != 4 {
		return 0, fmt.Errorf("regf: value %q has %d bytes, want 4 (REG_DWORD)", v.NameUTF8(), len(v.Data))
	}
	return binary.LittleEndian.Uint32(v.Data), nil
}

// SZ decodes v's data as a UTF-16LE string (the REG_SZ/REG_EXPAND_SZ
// convention this package's EncodeSZ produces: not NUL-terminated).
func (v *Value) SZ() string {
	return utf16leToString(v.Data)
}

// MultiSZ decodes v's data as a REG_MULTI_SZ value (see EncodeMultiSZ for
// the on-disk shape this expects).
func (v *Value) MultiSZ() []string {
	return decodeMultiSZ(v.Data)
}

// EncodeDWORD little-endian-encodes n as 4-byte REG_DWORD data, for use
// with SetValue(name, RegDWORD, EncodeDWORD(n)).
func EncodeDWORD(n uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, n)
	return b
}

// EncodeSZ encodes s as UTF-16LE data, for use with
// SetValue(name, RegSZ, EncodeSZ(s)) or RegExpandSZ. Not NUL-terminated;
// see SZ for the matching decoder.
func EncodeSZ(s string) []byte {
	return stringToUTF16LE(s)
}

// EncodeMultiSZ encodes strs as REG_MULTI_SZ data: each string as
// UTF-16LE followed by a UTF-16 NUL terminator, with the whole list
// terminated by one further (empty-string) NUL. For use with
// SetValue(name, RegMultiSZ, EncodeMultiSZ(strs)).
func EncodeMultiSZ(strs []string) []byte {
	var out []byte
	for _, s := range strs {
		out = append(out, stringToUTF16LE(s)...)
		out = append(out, 0, 0)
	}
	out = append(out, 0, 0)
	return out
}

// decodeMultiSZ is EncodeMultiSZ's reverse: splits b into its component
// UTF-16LE, NUL-terminated strings. Both the inter-string and final
// terminators are a UTF-16 NUL (0x0000); an empty/absent b decodes to nil.
func decodeMultiSZ(b []byte) []string {
	var out []string
	start := 0
	for i := 0; i+1 < len(b); i += 2 {
		if b[i] == 0 && b[i+1] == 0 {
			if i > start {
				out = append(out, utf16leToString(b[start:i]))
			}
			start = i + 2
		}
	}
	return out
}

// Package wim implements parsing, serialization, and the high-level structure
// of the WIM (Windows Imaging Format) container.
//
// It is a Go reimplementation of the on-disk format handling in wimlib
// (https://wimlib.net, https://github.com/ebiggers/wimlib), covering the parts
// of the format needed to read and write the container skeleton: the header,
// the blob (lookup) table, resource descriptors, image metadata resources
// (security data + directory-entry tree), and the XML data.
//
// Scope: this package handles the *structure* of a WIM. It deliberately does
// not implement the LZX / XPRESS / LZMS compression codecs, nor filesystem
// capture/apply. Compressed resource payloads are exposed as raw byte ranges;
// serialization writes resources uncompressed. See ResourceHeader.Flags and
// the WIM_RESHDR_FLAG_* constants for how compression is signalled on disk.
//
// It also provides path-based operations over a DirEntry tree, generalizing
// the path-walking/case-insensitive-child-lookup logic that callers like
// driver/install.go would otherwise hand-roll themselves: DirEntry.Lookup and
// DirEntry.Child resolve '/'- or '\'-separated paths case-insensitively (see
// ErrNotFound); DirEntry.Add and DirEntry.Remove create or delete a path's
// entry (and, for Remove, its subtree), creating intervening directories as
// Add needs them; DirEntry.Rename moves an entry; DirEntry.ReadDir lists a
// directory's children; and MatchName does DOS-style '*'/'?' glob matching
// over a single name component. As with the rest of this package, these stay
// within the structural layer: Reader.ReadFile resolves a path down to bytes
// but still returns ErrCompressedResource unmodified for a compressed blob
// rather than decompressing it, Add takes a Hash rather than raw content
// bytes (getting those bytes into a BlobTable is the caller's job, as it
// already is for driver.Install), and Remove does not adjust any BlobTable's
// reference counts for streams a removed subtree stops referencing.
package wim

import (
	"encoding/binary"
	"fmt"
)

// SHA1Size is the length in bytes of a SHA-1 message digest, used throughout
// the format to identify blob contents.
const SHA1Size = 20

// Hash is a SHA-1 message digest of a blob's uncompressed contents. The
// all-zero Hash is the conventional value for zero-length data.
type Hash [SHA1Size]byte

// IsZero reports whether h is the all-zero hash (used for empty blobs).
func (h Hash) IsZero() bool {
	for _, b := range h {
		if b != 0 {
			return false
		}
	}
	return true
}

// String returns the lowercase hex encoding of the hash.
func (h Hash) String() string {
	const hexdigits = "0123456789abcdef"
	var buf [SHA1Size * 2]byte
	for i, b := range h {
		buf[i*2] = hexdigits[b>>4]
		buf[i*2+1] = hexdigits[b&0x0f]
	}
	return string(buf[:])
}

// GUIDSize is the length in bytes of a WIM GUID.
const GUIDSize = 16

// GUID is the globally unique identifier stored in the WIM header. It is just
// 16 bytes of (normally random) data; the format assigns no internal structure.
type GUID [GUIDSize]byte

// byte order used everywhere in the WIM on-disk format.
var le = binary.LittleEndian

// wrapErr is a small helper for adding context to parse errors without pulling
// in a dependency.
func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("wim: %s: %w", what, err)
}

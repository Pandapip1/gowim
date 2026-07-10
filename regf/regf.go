// Package regf implements parsing and serialization of the on-disk structure
// of Windows Registry hive files (the "regf" format): SYSTEM, SOFTWARE, SAM,
// DEFAULT, NTUSER.DAT and similar files found e.g. at
// \Windows\System32\config\* inside a Windows image, or a loaded user hive.
//
// It is a Go reimplementation of the format described in Joachim Metz's
// "Windows NT Registry File (REGF) format specification" from the libregf
// project (https://github.com/libyal/libregf, file
// documentation/Windows NT Registry File (REGF) format.asciidoc), as fetched
// at commit fa7ed12674b308db80a003733e748fbcba4b6e4c (2026-06-25), document
// revision 0.0.31. Cell offsets, field layouts, and the LH subkey-list hash
// algorithm below are cited from that document by section name. Individual
// files in this package cite the specific section they implement.
//
// Scope: this package handles the format version 1.2-and-later on-disk
// layout (CM_KEY_NODE / CM_KEY_VALUE shapes), which is what every hive
// produced by Windows NT 3.51 and later (including all currently supported
// Windows versions) uses -- the base block, hive bin framing, cell framing
// (allocated vs. free, size, 8-byte alignment), named-key (nk), value (vk),
// security (sk) cells, all four subkey-list cell shapes (lf/lh/li/ri) well
// enough to enumerate a key's subkeys, and value data including out-of-line
// cells and "db" big-data reassembly. Parse builds a plain in-memory Key tree
// (mirroring wim.DirEntry's shape) from a raw hive byte slice, and AppendTo
// serializes such a tree back to valid regf bytes.
//
// It deliberately does NOT implement:
//
//   - The version 1.1 on-disk layout (seen only on Windows NT 3.1/3.5, whose
//     nk/vk/sk cells carry an extra leading 4-byte unknown field before their
//     signature). Real, currently-relevant hives are all version 1.2 or
//     later; see the "Format versions" section of the spec.
//   - Parsing the internal structure of a security descriptor's ACL/ACE
//     bytes (the SDDL-equivalent binary format). An sk cell's descriptor is
//     preserved as an opaque, round-trippable byte blob (Key.Security),
//     exactly like the sibling cat package's stance on X.509 certificates:
//     crypto/x509 (or a dedicated SD parser) can be layered on top by a
//     caller that needs it, but this package does not interpret the bytes.
//   - Transaction log replay (.LOG/.LOG1/.LOG2 files, and the dirty-page /
//     sequence-number recovery mechanism they support). This package reads
//     and writes a clean primary hive file only. The sequence-number and
//     checksum fields of the base block are still modeled (BaseBlock.
//     PrimarySequence, SecondarySequence, Checksum) so a caller can inspect
//     or set them, but no log-replay logic is implemented.
//   - Byte-for-byte reproduction of an arbitrary real-world hive's bin/cell
//     allocation layout on serialization. Windows' own allocator's specific
//     free-space fragmentation, cell ordering, and multi-bin packing is an
//     implementation detail this package does not reverse-engineer (compare
//     pe/image.go's analogous stance on non-critical PE padding regions).
//     Instead: (a) a hive produced by this package's own AppendTo parses
//     back with Parse byte-identically (a "tight" round trip, since the
//     layout is then simple and deterministic by construction), and (b)
//     Hive.AppendTo always builds a fresh, valid hive from an in-memory Key
//     tree via a single-hbin, simply-packed allocation strategy documented
//     on Hive.AppendTo in hive.go -- it does not attempt to match some
//     pre-existing hive's original bin/cell layout.
//   - Any higher-level "populate the registry with a driver's entries"
//     semantics (DriverDatabase, CriticalDeviceDatabase, INFCACHE.1, etc.).
//     That orchestration is future work for the sibling driver package, not
//     this one.
//   - Any live registry API semantics (opening HKLM, transactions,
//     notifications). This is purely an offline on-disk file-format package,
//     like wim/inf/cat/pe.
package regf

import (
	"encoding/binary"
	"fmt"
)

// le is the byte order used everywhere in the regf on-disk format.
var le = binary.LittleEndian

// NoCellOffset is the sentinel value used throughout the format to mean "no
// such cell" in an offset field (e.g. an empty subkey list, values list,
// security cell, or class name). See e.g. the "Named key" section's Sub
// keys list offset field: "Refers to a sub keys list or contains -1
// (0xffffffff) if empty."
const NoCellOffset uint32 = 0xffffffff

// HBinDataStart is the fixed size of the base block (the file header block),
// which is also the origin cell offsets are relative to: every on-disk cell
// offset is in bytes relative to the start of the hive bins data area, i.e.
// the absolute file offset is HBinDataStart + cellOffset. See the base
// block's "root key offset" field and the "file offset" formula in the
// "File header" section.
const HBinDataStart = 0x1000

// wrapErr is a small helper for adding context to parse errors without
// pulling in a dependency.
func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("regf: %s: %w", what, err)
}

// alignUp8 rounds n up to the next multiple of 8, matching the format's
// 8-byte cell-size alignment rule (see "Hive bin cell": "The size is 8 byte
// aligned").
func alignUp8(n int) int { return (n + 7) &^ 7 }

// cloneBytes returns a fresh copy of b, so parsed structures do not alias the
// input buffer.
func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

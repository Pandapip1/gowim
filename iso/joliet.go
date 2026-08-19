// Joliet: a Supplementary Volume Descriptor (ECMA-119 8.5) carrying a
// second, UCS-2-encoded directory hierarchy and Path Table Group alongside
// the primary ECMA-119 Level 1-3 tree, sharing the same file data extents.
//
// Joliet was never standardized by Ecma or ISO — it is a Microsoft-authored
// extension, only "spottily documented by Microsoft" in the words of the
// real implementation this package cross-checks against:
// cdrkit-1.1.11/genisoimage/joliet.c (cached from earlier phases at
// /tmp/claude/repos/cdrkit-1.1.11/genisoimage/joliet.c), by Eric Youngdale
// (1997) with later changes by J. Schilling. That file's opening comment is
// the closest thing to a specification consulted here:
//
//	"The SVD is identical to the PVD, except:
//		Id is 2, not 1 (indicates SVD).
//		escape_sequences contains UCS-2 indicator (levels 1, 2 or 3).
//		The root directory record points to a different extent (with
//			different size).
//		There are different path tables for the two sets of directory
//			trees."
//
// This package writes UCS-2 Level 3, which is what genisoimage defaults to
// (`int ucs_level = 3;` in genisoimage.c) and what every other producer
// measured for the UDF phase (real Windows/NTLite media) turned out to
// carry when it carries Joliet at all — see the "Escape Sequences" doc
// comment on writeJolietSVD.
//
// The identifier-mangling rules (mangleJolietName, name.go) and the
// per-node Joliet state (node.jolietID, node.jolietDirExtent/DirLength,
// tree.go) are documented where they are declared. This file holds the
// Supplementary Volume Descriptor itself and the three Joliet-specific
// buildLayout fragments; the directory/path-table serialisation is shared
// with the ECMA-119 tree through the hierarchyView mechanism in layout.go
// and write.go.
//
// # A note on what is deliberately not replicated from genisoimage
//
// genisoimage's Joliet tree is sorted independently of the ECMA-119 tree
// (joliet_compare_paths, joliet_compare_dirs), by the raw UCS-2 value of
// the *original* host name rather than the mangled 8.3-ish identifier. This
// package does not do that: both hierarchies here share exactly one
// traversal order — the one mangle() already produced by ECMA-119 9.3 rules
// — for both the Path Table record sequence and each directory's child
// order. This is a real, deliberate divergence from genisoimage, made
// because:
//
//   - ECMA-119 6.9.1 and 9.3's ordering requirements exist so that a reader
//     doing an ordered/binary search over the Path Table or a directory's
//     records can find an entry without a full scan; nothing in the Joliet
//     "specification" (such as it is) imposes an equivalent requirement,
//     since Joliet was never normatively specified at all.
//   - Every reader this package's Joliet output was validated against
//     (isoinfo -J, 7z l, xorriso, see the TODO.md entry for this item) reads
//     a Joliet directory or Path Table by linear scan and shows correct
//     names and sizes regardless of order.
//   - Implementing a second, independent sort (case-sensitive, UCS-2-value,
//     with its own root-first/"."/".." special-casing per
//     joliet_compare_dirs) is real additional code for a purely cosmetic
//     property — sibling order in a directory listing — that costs nothing
//     in correctness and buys only byte-for-byte parity with genisoimage's
//     own Joliet output, which is explicitly not this item's goal (compare
//     the Enhanced Volume Descriptor, which this item's own text says is
//     skipped for the same reason).
//
// Consequently a structural comparison against genisoimage -J's Joliet Path
// Table and directory extents will show entries in different relative
// order when a directory's ECMA-119-mangled name order differs from its
// original long-name order (for example when two names differ only after
// truncation). The content — the identifiers, extents and sizes — matches;
// only the sequence does not. This is recorded in TODO.md as a verified,
// harmless divergence rather than a bug.
package iso

import (
	"time"
	"unicode/utf16"
)

// jolietFileStructureVersion is the File Structure Version of a
// Supplementary Volume Descriptor whose Volume Descriptor Version (8.5.2)
// is 1: "For a Supplementary Volume Descriptor ... 1 shall indicate the
// structure of this Standard" (ECMA-119 8.5.2, applied via 8.4.30's cross
// reference). Only an Enhanced Volume Descriptor (Volume Descriptor
// Version 2) uses File Structure Version 2; this package does not write
// one (see the package doc's "What is deliberately NOT implemented yet").
const jolietFileStructureVersion = 1

// jolietEscapeSequence is the UCS-2 Level 3 escape sequence recorded in the
// Supplementary Volume Descriptor's Escape Sequences field (ECMA-119
// 8.5.6).
//
// Source: cdrkit-1.1.11/genisoimage/joliet.c, lines 58-64's comment table
// ("UCS-2 Level-3 -> ASCII escape code %/E") and line 112-117's
// `ucs_codes[]` array (`'\0', '@', 'C', 'E'` for levels 0-3), combined at
// line 448 as `sprintf(jvol_desc->escape_sequences, "%%/%c",
// ucs_codes[ucs_level])` with the default `ucs_level = 3`
// (genisoimage.c line 177). "%%/%c" is a C format string whose "%%"
// escapes to a literal '%', so the three bytes actually written are
// 0x25 ('%'), 0x2F ('/'), 0x45 ('E'). This is the well-known "level 3"
// escape sequence used to identify Joliet in practice; every Joliet-aware
// reader this package was validated against (isoinfo, 7z, xorriso) expects
// exactly this sequence, and no other value was ever seen during the
// UDF-phase media survey referenced in TODO.md.
var jolietEscapeSequence = [3]byte{0x25, 0x2F, 0x45}

// writeJolietSVD emits the Joliet Supplementary Volume Descriptor.
//
// Per joliet.c's own summary quoted in this file's package comment, "The
// SVD is identical to the PVD, except" a small set of fields — this
// function is therefore laid out to mirror writePVD field for field, with
// the differences called out inline. Two divergences worth noting, both
// verified against joliet.c rather than assumed:
//
//   - The textual identifier fields (System, Volume, Volume Set, Publisher,
//     Preparer, Application Identifier) hold the *same already-sanitized*
//     a-character/d-character content as the Primary Volume Descriptor,
//     re-encoded as UCS-2BE, not a separately-cased Joliet-only label.
//     get_joliet_vol_desc (joliet.c line 435) starts from `jvol_desc =
//     vol_desc`, a raw copy of the already-built Primary Volume Descriptor,
//     and only overwrites specific fields; convert_to_unicode is then
//     called with a NULL source, meaning "convert this field in place",
//     i.e. it UCS-2-encodes the PVD's own already-FILLER-padded a/d-char
//     bytes rather than re-deriving anything from the caller's original
//     string. This package matches that for field-for-field comparability
//     against genisoimage's own output; a "real" Joliet-only volume label
//     preserving the caller's original case would need a separate Options
//     field, which is not exposed here.
//   - Those fields are also only half as many *characters* as their PVD
//     counterparts, because convert_to_unicode converts in place within a
//     field of the *same byte width* as the PVD's (e.g. 32 bytes for the
//     System/Volume Identifier, per ECMA-119 8.5.4/8.5.5 citing the same BP
//     ranges, 9-40 and 41-72, as 8.4.5/8.4.6): its loop bound is
//     `(i + 1) < size` with `size = sizeof(field)`, so only the first
//     size/2 source characters are ever consulted. This is the well-known
//     real-world Joliet volume-label limit (16 characters for a 32-byte
//     field), reproduced here as jolietEncodeField's byte-width halving
//     rather than asserted from memory.
func (l *layout) writeJolietSVD(w *sectorWriter) error {
	o := l.b.opts
	s := w.sector()

	put711(s[0:1], 2)     // 8.5.1 Volume Descriptor Type: 2 = Supplementary/Enhanced
	copy(s[1:6], "CD001") // 8.5's Standard Identifier, same field as 8.4.2
	put711(s[6:7], 1)     // 8.5.2 Volume Descriptor Version: 1 for a Supplementary VD
	put711(s[7:8], 0)     // 8.5.3 Volume Flags: bit 0 clear, the escape sequence below is ISO 2375-registered
	jolietEncodeField(s[8:40], sanitizeAChars(o.SystemID))
	jolietEncodeField(s[40:72], sanitizeDChars(o.VolumeID))
	putZero(s[72:80])                // 8.5's Unused Field, same as 8.4.7
	put733(s[80:88], l.totalSectors) // Volume Space Size: one volume, so identical to the PVD's
	putZero(s[88:120])
	copy(s[88:91], jolietEscapeSequence[:]) // 8.5.6 Escape Sequences
	put723(s[120:124], 1)                   // Volume Set Size
	put723(s[124:128], 1)                   // Volume Sequence Number
	put723(s[128:132], LogicalSectorSize)   // Logical Block Size
	put733(s[132:140], jolietView.pathTableSize(l))

	put731(s[140:144], l.fragStart("Joliet Type L Path Table"))
	put731(s[144:148], 0)
	put732(s[148:152], l.fragStart("Joliet Type M Path Table"))
	put732(s[152:156], 0)

	// 8.5.12 Directory Record for Root Directory: the Joliet root's own
	// record, not the ECMA-119 root's — it names a different extent with a
	// different length, per joliet.c's own summary quoted above.
	if err := writeDirectoryRecordV(s[156:190], jolietView, l.dirs[0], selfRecord, o); err != nil {
		return err
	}

	jolietEncodeField(s[190:318], sanitizeDChars(o.VolumeSetID))
	jolietEncodeField(s[318:446], sanitizeAChars(o.PublisherID))
	jolietEncodeField(s[446:574], sanitizeAChars(o.PreparerID))
	jolietEncodeField(s[574:702], sanitizeAChars(o.ApplicationID))

	// 8.5.17 to 8.5.19: as in the PVD, no such files are identified.
	jolietEncodeField(s[702:739], "")
	jolietEncodeField(s[739:776], "")
	jolietEncodeField(s[776:813], "")

	putLongDateTime(s[813:830], o.Timestamp)
	putLongDateTime(s[830:847], o.Timestamp)
	putLongDateTime(s[847:864], time.Time{})
	putLongDateTime(s[864:881], o.Timestamp)

	put711(s[881:882], jolietFileStructureVersion) // 8.5's File Structure Version field, same BP as 8.4.30
	putZero(s[882:883])
	putZero(s[883:1395])
	putZero(s[1395:2048])

	return w.write(s)
}

// jolietEncodeField encodes s as UCS-2BE into a field exactly len(dst)
// bytes wide, per the doc comment on writeJolietSVD: only the first
// len(dst)/2 characters of s are used, and any remainder is padded with
// FILLER (ECMA-119 7.4.3.2/7.4.5), which in UCS-2 is the code unit 0x0020
// recorded as the two bytes (00)(20).
func jolietEncodeField(dst []byte, s string) {
	units := utf16.Encode([]rune(s))
	max := len(dst) / 2
	if len(units) > max {
		units = units[:max]
	}
	i := 0
	for ; i < len(units); i++ {
		u := jolietSanitizeUnit(units[i])
		dst[2*i], dst[2*i+1] = byte(u>>8), byte(u)
	}
	for ; i < max; i++ {
		dst[2*i], dst[2*i+1] = 0x00, 0x20
	}
}

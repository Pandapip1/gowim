// Package iso writes ISO 9660 (ECMA-119) CD-ROM filesystem images.
//
// The eventual goal of this package is to author bootable Windows
// installation media byte-for-byte equivalent in *function* (not
// necessarily in layout) to what Microsoft's oscdimg and the cdrkit
// genisoimage tool produce, so that gowim can finish a rebuilt Windows
// image without shelling out to an external ISO authoring tool. Windows
// install media is not plain ISO 9660: it is a hybrid ("bridge") volume
// carrying ISO 9660, UDF and an El Torito boot catalog over one shared set
// of file extents. This package is being built in phases, and the phase
// boundaries are documented under "Scope" below so that callers are never
// misled about what is actually implemented.
//
// # Sources
//
// Nothing in this package is written from memory or from plausibility. Each
// structure below is taken either from the normative standard or from a
// real, shipping implementation, and the doc comment on each type cites the
// clause or the source file it came from.
//
//   - ECMA-119, "Volume and File Structure of CDROM for Information
//     Interchange", 4th edition (June 2019), published freely by Ecma
//     International. This is the normative reference and is the same
//     document as ISO 9660 plus its later amendments. Clause numbers cited
//     throughout this package ("ECMA-119 9.1.6") refer to this edition. The
//     4th edition was used rather than the original 2nd edition (December
//     1987) because it folds in the ISO 9660:1999 / "version 2" Enhanced
//     Volume Descriptor (8.5, 8.4.30) that genisoimage's -iso-level 4 emits
//     and that a later phase of this package will need.
//
//   - cdrkit 1.1.11 (Debian source package cdrkit_1.1.11.orig.tar.gz), the
//     genisoimage program that currently produces this project's known-good
//     bootable Windows 11 ISO. It is the highest-value cross-check
//     available because its output can be diffed against this package's.
//     Files consulted: genisoimage/iso9660.h (on-disk structure layout),
//     genisoimage/genisoimage.c (option semantics and, critically, the
//     image-wide fragment ordering built by the outputlist_insert calls
//     near line 3517 onwards), genisoimage/write.c (the output_fragment
//     mechanism), genisoimage/tree.c (name mangling, and the >4 GiB
//     handling at line 1554), genisoimage/udf.c and genisoimage/udf_fs.h
//     (the UDF layer, the latter being a complete on-disk field map),
//     genisoimage/eltorito.c and genisoimage/bootinfo.h (El Torito and the
//     boot information table).
//
//   - The El Torito "Bootable CD-ROM Format Specification" version 1.0
//     (Phoenix Technologies / IBM, 1995) and UEFI 2.10 section 13.3.2.1, for
//     the boot catalog. See eltorito.go's file comment, which cites them
//     figure by figure and explains why the UEFI Platform ID 0xEF is not an
//     El Torito value at all.
//
//   - ECMA-167 3rd edition (June 1997) and the OSTA Universal Disk Format
//     Specification revision 1.02, for the UDF layer. See udf.go's
//     file-level comment, which cites them clause by clause.
//
//   - cdrkit 1.1.11's genisoimage/joliet.c again, for the Joliet layer,
//     since Joliet was never standardized by Ecma or ISO. See joliet.go's
//     file-level comment.
//
// # What is implemented
//
//   - The Primary Volume Descriptor (ECMA-119 8.4) and the Volume
//     Descriptor Set Terminator (8.3).
//   - Directory Records (9.1), including the reserved "." and ".." records
//     required by 6.8.2.2, sorted per 9.3.
//   - Type L and Type M Path Tables (9.4), ordered per 6.9.1.
//   - Extent allocation over a 2048-byte logical sector / logical block.
//   - Multiple File Sections per file (the ECMA-119 6.5.1 "multi-extent"
//     mechanism, signalled by the File Flags bit 7 of 9.1.6), which is the
//     standard-conformant way to record a file larger than the 32-bit Data
//     Length field of 9.1.4 allows. This requires interchange Level 3
//     (10.3); Levels 1 and 2 forbid it (10.1, 10.2). Flagged as UNVERIFIED
//     against real readers: no ISO on hand uses it.
//   - The UDF bridge layer (Options.UDF), which is what Windows installation
//     media actually relies on and what makes files of 4 GiB or more
//     possible. It describes the same file extents the ECMA-119 layer
//     assigned; nothing is written twice. See udf.go.
//   - El Torito (Options.BootEntries): the Boot Record Volume Descriptor of
//     ECMA-119 8.2 immediately after the Primary Volume Descriptor, and a
//     boot catalog recorded as an ordinary file, with one Initial/Default
//     Entry and a Section Header plus Section Entry per further platform.
//     Optionally genisoimage's boot information table — patched into the
//     output stream only, never into the caller's file. See eltorito.go.
//   - Joliet (Options.Joliet): a Supplementary Volume Descriptor (ECMA-119
//     8.5) with a UCS-2 Level 3 escape sequence (8.5.6), and a second,
//     parallel directory hierarchy and Path Table Group recording the
//     caller's original long, mixed-case names in UCS-2BE, sharing the
//     same file data extents as the ECMA-119 tree. This is what lets a
//     plain ISO 9660 reader show real names instead of the 8.3-ish
//     upper-cased, version-suffixed ones the ECMA-119 tree alone produces.
//     See joliet.go.
//
// # What is deliberately NOT implemented yet
//
// The layout mechanism in layout.go reserved an insertion point for this
// from phase 1 onward, but it is not written:
//
//   - The ISO 9660:1999 Enhanced Volume Descriptor that genisoimage's
//     -iso-level 4 emits (8.5 with File Structure Version 2 per 8.4.30).
//     Without it, the ECMA-119 tree's own identifiers stay d-characters,
//     upper-cased and version-suffixed, where genisoimage's -iso-level 4
//     preserves them. UDF and Joliet each carry the real names already,
//     which is what Windows media (UDF) and every other reader (Joliet)
//     actually rely on; the Enhanced Volume Descriptor would only matter
//     for matching genisoimage's -iso-level 4 output field for field.
//
// # Scope boundary
//
// This package deals only with laying out and serialising the filesystem
// structures. It does not read ISO images, does not burn or verify media,
// and knows nothing about WIM, PE or any other gowim format: callers hand
// it a tree of names and content Sources and receive a byte stream.
//
// # Why the layout is a list of fragments
//
// The single most important design constraint on this package is that a
// later UDF phase had to be bolt-on rather than a rewrite. In a bridge
// volume the ISO 9660 directory records and the UDF file entries describe
// the *same* file data extents, so file data cannot simply be appended
// wherever the ISO 9660 layer feels like it; UDF metadata occupies fixed
// early sectors (and ECMA-167 additionally pins Anchor Volume Descriptor
// Pointers to specific sectors near the start and end of the volume), and
// so must be reserved before any file data is placed.
//
// genisoimage solves this with an ordered list of "output fragments", each
// of which is first asked how many sectors it needs (assigning it a start
// LBA) and only later asked to write its bytes; the UDF fragments are
// inserted into that list ahead of the path tables, directory tree and file
// data (genisoimage/genisoimage.c, the outputlist_insert sequence). This
// package deliberately copies that shape — see layout.go — because it is
// the structure that makes the later phases additive. That prediction held:
// adding UDF meant inserting fragments at the right position in the list
// (layout.go's addUDFHead and addUDFTail) and changed nothing about how
// extents are assigned. El Torito was the same shape of change again: one
// fragment for the Boot Record Volume Descriptor, and a boot catalog that is
// simply a file in the tree whose bytes are generated once every extent is
// known. Joliet was the same again: one fragment for the Supplementary
// Volume Descriptor and two more pairs of fragments (a Path Table Group and
// a directory tree) alongside the ECMA-119 ones, sharing the file data
// fragment and the extents it already assigned. The directory-record and
// path-table serialisation itself is not forked for Joliet: layout.go's
// hierarchyView parameterises the one implementation over which identifier
// encoding and which pair of per-directory extent fields to use.
package iso

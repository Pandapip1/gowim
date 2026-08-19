package iso

import (
	"fmt"
	"math"
)

// fragment is one contiguous, sector-aligned region of the image.
//
// The image is described as an ordered list of fragments. Layout runs in
// two passes over that list: the first asks each fragment how many Logical
// Sectors it occupies and assigns it a start LBA; the second asks each
// fragment to produce its bytes, by which time every LBA in the image is
// known and cross-references can be filled in.
//
// This is deliberately the same shape as genisoimage's output_fragment list
// (cdrkit-1.1.11/genisoimage/write.c, and the outputlist_insert sequence in
// genisoimage.c around line 3517), for the reason given in the package doc:
// a UDF phase must be able to reserve its fixed early sectors by inserting
// fragments ahead of the path tables and file data, without changing how
// anything else is allocated.
type fragment struct {
	// name identifies the fragment in error messages and in the layout
	// dump that tests use to compare against genisoimage's own
	// "Total extents" accounting.
	name string
	// sectors is the fragment's length in Logical Sectors, filled in by
	// the sizing pass.
	sectors uint32
	// start is the Logical Sector Number of the fragment's first sector,
	// filled in by the assignment pass.
	start uint32
	// size computes sectors. It runs before any LBA is known, so it must
	// not depend on the position of any other fragment.
	size func(*layout) (uint32, error)
	// write emits exactly sectors*LogicalSectorSize bytes. It runs after
	// every LBA is assigned.
	write func(*layout, *sectorWriter) error
}

// layout holds the state shared by the sizing and writing passes.
type layout struct {
	b     *Builder
	dirs  []*node
	files []*node

	frags []*fragment
	// cur is the fragment currently being sized; see currentFragmentStart.
	cur *fragment

	// udf holds the UDF layer's sector assignments, or nil when
	// Options.UDF is clear.
	udf *udfLayout

	// boot holds the El Torito layer's resolved boot images and catalog, or
	// nil when Options.BootEntries is empty.
	boot *bootLayout

	// Filled in by the sizing pass, because the Primary Volume Descriptor
	// has to record them (ECMA-119 8.4.13 to 8.4.17).
	pathTableSize uint32
	// jolietPathTableSize is pathTableSize's counterpart for the Joliet
	// Supplementary Volume Descriptor (8.5.7 to 8.5.11), filled in the same
	// way when Options.Joliet is set.
	jolietPathTableSize uint32

	// totalSectors is the Volume Space Size in Logical Blocks (8.4.8),
	// known once every fragment has been assigned.
	totalSectors uint32
}

// buildLayout assembles the ordered fragment list for the image.
//
// The order below is normative in places and conventional in others:
//
//   - The System Area must be sectors 0 to 15 (ECMA-119 6.2.1) and the
//     Volume Descriptor Set must start at sector 16 (6.3). Those are fixed.
//   - The Volume Descriptor Set must end with a Volume Descriptor Set
//     Terminator (8.3).
//   - Everything after that is at the producer's discretion. The order used
//     here — path tables, then directory extents, then file data — is
//     genisoimage's, so that the two producers' images can be compared
//     fragment by fragment.
//
// The commented insertion points are where the deferred phases go. They are
// written out explicitly rather than left implicit because the *position*
// is the part that is easy to get wrong later: genisoimage notes in
// genisoimage.c that the El Torito Boot Record "MUST be immediately after
// the PVD", and its UDF fragments are all inserted before the path tables.
func buildLayout(b *Builder) *layout {
	l := &layout{b: b, dirs: b.dirs(), files: b.files()}

	l.add(&fragment{
		name: "System Area",
		size: func(*layout) (uint32, error) { return systemAreaSectors, nil },
		// ECMA-119 6.2.1 leaves the content unspecified; zero-filled here.
		write: func(_ *layout, w *sectorWriter) error { return w.zeroSectors(systemAreaSectors) },
	})

	l.add(&fragment{
		name:  "Primary Volume Descriptor",
		size:  oneSector,
		write: (*layout).writePVD,
	})

	// The El Torito Boot Record Volume Descriptor goes immediately after the
	// Primary Volume Descriptor. That position is not merely conventional:
	// El Torito 1.0 section 1.4 says the Boot Record "must reside at sector
	// 11 (17 decimal) in the last session on the CD", i.e. exactly one sector
	// after the Primary Volume Descriptor at 16. genisoimage's genisoimage.c
	// notes the same in a comment on its outputlist_insert of torito_desc,
	// and both the reference image and Microsoft's own media put it at 17.
	if len(b.opts.BootEntries) > 0 {
		l.add(&fragment{
			name:  "El Torito Boot Record Volume Descriptor",
			size:  oneSector,
			write: (*layout).writeBootRecord,
		})
	}

	// Remaining deferred phase insertion point: the ISO 9660:1999 Enhanced
	// Volume Descriptor (8.5, File Structure Version 2 per 8.4.30), what
	// genisoimage -iso-level 4 emits. Not implemented; see the package doc.

	// The Joliet Supplementary Volume Descriptor goes here: immediately
	// after where the (unimplemented) Enhanced Volume Descriptor would go
	// and immediately before the Volume Descriptor Set Terminator. That is
	// genisoimage's own order (genisoimage.c's outputlist_insert sequence:
	// voldesc_desc, torito_desc, xvoldesc_desc, joliet_desc, end_vol) and
	// there is no normative reason to differ, since Joliet was never
	// standardized and ECMA-119 8.3/6.3 only fix the Terminator's role
	// (last) and the set's start (16), not what comes between.
	if b.opts.Joliet {
		l.add(&fragment{
			name:  "Joliet Supplementary Volume Descriptor",
			size:  oneSector,
			write: (*layout).writeJolietSVD,
		})
	}

	l.add(&fragment{
		name:  "Volume Descriptor Set Terminator",
		size:  oneSector,
		write: (*layout).writeTerminator,
	})

	if b.opts.UDF {
		l.addUDFHead()
	}

	l.add(&fragment{
		name:  "Type L Path Table",
		size:  pathTableSectors,
		write: func(l *layout, w *sectorWriter) error { return l.writePathTable(w, false) },
	})
	l.add(&fragment{
		name:  "Type M Path Table",
		size:  pathTableSectors,
		write: func(l *layout, w *sectorWriter) error { return l.writePathTable(w, true) },
	})

	// Joliet's own Path Table Group follows immediately, matching
	// genisoimage's jpathtable_desc placement right after pathtable_desc
	// (genisoimage.c's outputlist_insert sequence).
	if b.opts.Joliet {
		l.add(&fragment{
			name:  "Joliet Type L Path Table",
			size:  jolietPathTableSectors,
			write: func(l *layout, w *sectorWriter) error { return l.writePathTableV(w, jolietView, false) },
		})
		l.add(&fragment{
			name:  "Joliet Type M Path Table",
			size:  jolietPathTableSectors,
			write: func(l *layout, w *sectorWriter) error { return l.writePathTableV(w, jolietView, true) },
		})
	}

	l.add(&fragment{
		name:  "Directory tree",
		size:  directorySectors,
		write: (*layout).writeDirectories,
	})

	if b.opts.Joliet {
		l.add(&fragment{
			name:  "Joliet directory tree",
			size:  jolietDirectorySectors,
			write: func(l *layout, w *sectorWriter) error { return l.writeDirectoriesV(jolietView, w) },
		})
	}

	l.add(&fragment{
		name:  "File data",
		size:  fileDataSectors,
		write: (*layout).writeFileData,
	})

	if b.opts.UDF {
		l.addUDFTail()
	} else if b.opts.PadSectors > 0 {
		pad := b.opts.PadSectors
		l.add(&fragment{
			name:  "Trailing pad",
			size:  func(*layout) (uint32, error) { return pad, nil },
			write: func(_ *layout, w *sectorWriter) error { return w.zeroSectors(pad) },
		})
	}

	return l
}

// addUDFHead inserts the UDF metadata fragments that must precede all file
// data: the Volume Recognition Sequence, the two Volume Descriptor Sequences
// and the Logical Volume Integrity Sequence, the anchor at sector 256, the
// File Set Descriptor, and the per-directory and per-file metadata.
//
// The order and the two pad-to-sector fragments are genisoimage's
// (genisoimage.c's outputlist_insert block guarded by use_udf, which runs
// after end_vol and before pathtable_desc). It is not arbitrary:
//
//   - The Volume Recognition Sequence must butt directly against the
//     ECMA-119 Volume Descriptor Set with no gap, because ECMA-167 2/8.3.1
//     Note 1 ends the recognition sequence at the first sector that is not a
//     valid Volume Structure Descriptor.
//   - Everything else must be below sector 256, because the anchor lives
//     there and a reader that finds a Volume Descriptor Sequence extent from
//     it must find real descriptors.
//   - All of it must be sized before the ISO path tables, directory extents
//     and file data, because file extents have to be final before the UDF
//     allocation descriptors that share them can be written.
func (l *layout) addUDFHead() {
	l.udf = &udfLayout{}
	l.udf.dirs, l.udf.files = udfWalk(l.b.root)

	l.add(&fragment{
		name:  "UDF Volume Recognition Sequence",
		size:  func(*layout) (uint32, error) { return 3, nil },
		write: (*layout).writeUDFVolumeRecognitionSequence,
	})
	l.addPadTo("UDF pad to sector 32", udfVolumeDescriptorSequenceSector)
	l.add(&fragment{
		name: "UDF Main Volume Descriptor Sequence",
		size: func(l *layout) (uint32, error) {
			l.udf.mainSeq = l.currentFragmentStart()
			return udfMainSeqSectors, nil
		},
		write: func(l *layout, w *sectorWriter) error { return l.writeUDFMainSeq(w, l.udf.mainSeq) },
	})
	l.add(&fragment{
		name: "UDF Reserve Volume Descriptor Sequence",
		size: func(l *layout) (uint32, error) {
			l.udf.reserveSeq = l.currentFragmentStart()
			return udfMainSeqSectors, nil
		},
		write: func(l *layout, w *sectorWriter) error { return l.writeUDFMainSeq(w, l.udf.reserveSeq) },
	})
	l.add(&fragment{
		name: "UDF Logical Volume Integrity Sequence",
		size: func(l *layout) (uint32, error) {
			l.udf.integritySeq = l.currentFragmentStart()
			return udfIntegritySeqSectors, nil
		},
		write: (*layout).writeUDFIntegritySeq,
	})
	l.addPadTo("UDF pad to sector 256", udfAnchorSector)
	l.add(&fragment{
		name:  "UDF Anchor Volume Descriptor Pointer",
		size:  oneSector,
		write: func(l *layout, w *sectorWriter) error { return l.writeUDFAnchor(w, udfAnchorSector) },
	})
	l.add(&fragment{
		name: "UDF File Set Descriptor",
		size: func(l *layout) (uint32, error) {
			// The partition begins here: everything from this sector on is
			// addressed relative to it (ECMA-167 3/10.5.8, 4/7.1).
			l.udf.partitionStart = l.currentFragmentStart()
			return 2, nil
		},
		write: (*layout).writeUDFFileSetDesc,
	})
	l.add(&fragment{
		name:  "UDF directory tree",
		size:  (*layout).udfDirTreeSectors,
		write: (*layout).writeUDFDirTree,
	})
	l.add(&fragment{
		name:  "UDF file entries",
		size:  (*layout).udfFileEntriesSectors,
		write: (*layout).writeUDFFileEntries,
	})
}

// addPadTo inserts a fragment of zero sectors that runs up to, but not into,
// the given sector.
func (l *layout) addPadTo(name string, target uint32) {
	f := &fragment{name: name}
	f.size = func(l *layout) (uint32, error) {
		start := l.currentFragmentStart()
		if start > target {
			return 0, fmt.Errorf("iso: the volume descriptors already reach sector %d, "+
				"but UDF needs sector %d to be free", start, target)
		}
		return target - start, nil
	}
	f.write = func(_ *layout, w *sectorWriter) error { return w.zeroSectors(f.sectors) }
	l.add(f)
}

// addUDFTail inserts the closing Anchor Volume Descriptor Pointer, plus the
// run-out padding if any.
//
// ECMA-167 3/8.4.2.1 puts anchor points at sectors 256, N-256 and N, and UDF
// 1.02 2.2.3 requires two of the three. Measured reality on both
// genisoimage's and Microsoft's output: 256 and N, never N-256, which would
// mean a hole in the middle of the file data. Since the anchor has to be at
// or near the last recorded sector, the image's total length has to be
// settled before it is written — which is exactly what this fragment list
// does, since it is sized in order and the anchor is last.
func (l *layout) addUDFTail() {
	l.add(&fragment{
		name: "UDF End Anchor Volume Descriptor Pointer",
		size: func(l *layout) (uint32, error) {
			l.udf.endAnchor = l.currentFragmentStart()
			return 1, nil
		},
		write: func(l *layout, w *sectorWriter) error { return l.writeUDFAnchor(w, l.udf.endAnchor) },
	})
	if pad := l.b.opts.PadSectors; pad > 0 {
		f := &fragment{name: "UDF trailing anchors"}
		f.size = func(*layout) (uint32, error) { return pad, nil }
		f.write = func(l *layout, w *sectorWriter) error {
			return l.writeUDFTrailingAnchors(w, f.start, pad)
		}
		l.add(f)
	}
}

func (l *layout) add(f *fragment) { l.frags = append(l.frags, f) }

func oneSector(*layout) (uint32, error) { return 1, nil }

// assign runs the sizing pass and fixes every fragment's start LBA.
//
// The sizing functions for the path tables, the directory tree and the file
// data have a side effect: they also assign the per-directory and per-file
// extents. That is safe because each of those fragments is sized exactly
// once and in list order, so by the time a fragment is sized every fragment
// before it already has a start LBA. Extents therefore have to be handed
// out relative to the fragment's own base, which is why the sizing
// functions take the layout rather than being pure.
func (l *layout) assign() error {
	var next uint32
	for _, f := range l.frags {
		f.start = next
		l.cur = f
		n, err := f.size(l)
		if err != nil {
			return err
		}
		f.sectors = n
		if next+n < next {
			return fmt.Errorf("iso: image exceeds the 32-bit Logical Block Number space of ECMA-119 8.4.8")
		}
		next += n
	}
	l.totalSectors = next
	return nil
}

// hierarchyView abstracts the two structurally identical directory-record /
// path-table hierarchies this package can write over one shared set of file
// data extents: the primary ECMA-119 Level 1-3 tree (d-character
// identifiers, ";1" versions, isoView) and, when Options.Joliet is set, the
// parallel Joliet tree (UCS-2BE identifiers, no version numbers,
// jolietView; see joliet.go). Every function in this file and in write.go
// that lays out or serialises a directory hierarchy or a Path Table Group
// is written once, against this interface, rather than forked per
// hierarchy — only the identifier encoding and the per-directory extent
// bookkeeping actually differ between the two.
//
// Both hierarchies share l.dirs (the same traversal order and the same
// node.pathIndex numbering) rather than each computing its own: see
// node.jolietDirExtent's doc comment for why that is a deliberate
// simplification rather than an oversight.
type hierarchyView struct {
	// name identifies the hierarchy in error messages.
	name string
	// ownID returns the Directory Identifier / File-Identifier-without-
	// version bytes for a node's own entry in its parent's directory: what
	// a Path Table Record's Directory Identifier field (9.4.5) holds, and,
	// for a directory child, its Directory Record's File Identifier too.
	ownID func(n *node) []byte
	// fileID returns the complete File Identifier field (9.1.11) for a
	// file's Directory Record, including any version-number suffix.
	fileID func(n *node) []byte
	// extent and length read and write a directory's own extent location
	// and byte length in this hierarchy (node.dirExtent/dirLength for
	// isoView, node.jolietDirExtent/jolietDirLength for jolietView).
	extent    func(n *node) uint32
	setExtent func(n *node, v uint32)
	length    func(n *node) uint32
	setLength func(n *node, v uint32)
	// pathTableSize returns this hierarchy's cached Path Table byte size
	// (l.pathTableSize or l.jolietPathTableSize), filled in by the sizing
	// pass before any write-pass function runs.
	pathTableSize func(l *layout) uint32
}

// isoView is the hierarchyView for the primary ECMA-119 Level 1-3 tree.
var isoView = hierarchyView{
	name:          "ECMA-119",
	ownID:         func(n *node) []byte { return []byte(n.id) },
	fileID:        func(n *node) []byte { return []byte(fileIdentifier(n)) },
	extent:        func(n *node) uint32 { return n.dirExtent },
	setExtent:     func(n *node, v uint32) { n.dirExtent = v },
	length:        func(n *node) uint32 { return n.dirLength },
	setLength:     func(n *node, v uint32) { n.dirLength = v },
	pathTableSize: func(l *layout) uint32 { return l.pathTableSize },
}

// jolietView is the hierarchyView for the Joliet tree (joliet.go).
var jolietView = hierarchyView{
	name:          "Joliet",
	ownID:         func(n *node) []byte { return n.jolietID },
	fileID:        func(n *node) []byte { return n.jolietID },
	extent:        func(n *node) uint32 { return n.jolietDirExtent },
	setExtent:     func(n *node, v uint32) { n.jolietDirExtent = v },
	length:        func(n *node) uint32 { return n.jolietDirLength },
	setLength:     func(n *node, v uint32) { n.jolietDirLength = v },
	pathTableSize: func(l *layout) uint32 { return l.jolietPathTableSize },
}

// pathTableID returns v's Path Table Directory Identifier for d.
//
// ECMA-119 6.8.2.2 and 9.4.5: the record for the Root Directory carries a
// Directory Identifier consisting of a single (00) byte, the same reserved
// identifier the Root Directory's own first Directory Record uses in both
// hierarchies (see writeDirectoryRecordV's selfID).
func (v hierarchyView) pathTableID(d *node) []byte {
	if d.parent == nil {
		return selfID
	}
	return v.ownID(d)
}

// pathTableSectors sizes the ECMA-119 Path Table. See pathTableSectorsV.
func pathTableSectors(l *layout) (uint32, error) {
	return pathTableSectorsV(isoView, &l.pathTableSize)(l)
}

// jolietPathTableSectors sizes the Joliet Path Table.
func jolietPathTableSectors(l *layout) (uint32, error) {
	return pathTableSectorsV(jolietView, &l.jolietPathTableSize)(l)
}

// pathTableSectorsV sizes one hierarchy's Path Table.
//
// A Path Table Record is 8 bytes of fixed fields plus the Directory
// Identifier, plus a (00) padding byte when the identifier length is odd
// (ECMA-119 9.4, 9.4.6). Unlike Directory Records, Path Table Records are
// *not* forbidden from spanning a Logical Sector boundary — 6.8.1.1's
// end-in-the-same-sector rule is stated for directories only — so the table
// is simply a byte stream padded out to a whole number of sectors.
//
// Both Type L and Type M tables have the same size, so the value is
// computed once and cached in *size, which is also what the owning Volume
// Descriptor's Path Table Size field (8.4.13/8.5.7) records.
func pathTableSectorsV(v hierarchyView, size *uint32) func(*layout) (uint32, error) {
	return func(l *layout) (uint32, error) {
		if *size == 0 {
			var total uint64
			for _, d := range l.dirs {
				total += uint64(pathTableRecordLenBytes(len(v.pathTableID(d))))
			}
			if total > math.MaxUint32 {
				return 0, fmt.Errorf("iso: %s path table too large", v.name)
			}
			*size = uint32(total)
		}
		return sectorsFor(uint64(*size)), nil
	}
}

func pathTableRecordLenBytes(idLen int) int {
	return 8 + idLen + idLen%2
}

// directorySectors sizes the ECMA-119 directory tree. See
// directorySectorsV.
func directorySectors(l *layout) (uint32, error) { return directorySectorsV(isoView)(l) }

// jolietDirectorySectors sizes the Joliet directory tree.
func jolietDirectorySectors(l *layout) (uint32, error) { return directorySectorsV(jolietView)(l) }

// directorySectorsV sizes one hierarchy's directory tree and assigns each
// directory its extent in that hierarchy.
//
// Each directory's extent begins with the two reserved records of ECMA-119
// 6.8.2.2 — "." identifying the directory itself, with a Directory
// Identifier of a single (00) byte, and ".." identifying its Parent
// Directory, with a single (01) byte — followed by one Directory Record per
// child in 9.3 order (or, for jolietView, the same order: see
// node.jolietDirExtent's doc comment).
//
// Records are packed into Logical Sectors under 6.8.1.1: a record that
// would not fit in the remainder of the current sector starts the next
// sector instead, and the unused bytes are set to (00). Per 6.8.1.3 the
// recorded length of the directory includes that trailing slack, so the
// length is always a whole number of sectors.
func directorySectorsV(v hierarchyView) func(*layout) (uint32, error) {
	return func(l *layout) (uint32, error) {
		base := l.currentFragmentStart()
		next := base
		for _, d := range l.dirs {
			n, err := directoryExtentSectorsV(v, d)
			if err != nil {
				return 0, err
			}
			v.setExtent(d, next)
			v.setLength(d, n*LogicalSectorSize)
			next += n
		}
		return next - base, nil
	}
}

func directoryExtentSectorsV(v hierarchyView, d *node) (uint32, error) {
	// The "." and ".." records: LEN_FI is 1 (odd), so 9.1.12's padding
	// field is absent and LEN_DR is 33 + 1 = 34. That is exactly the size
	// of the "Directory Record for Root Directory" field of the Primary
	// Volume Descriptor (8.4.18, BP 157 to 190) and, identically, of the
	// Supplementary Volume Descriptor (8.5.12, BP 157 to 190).
	used := uint32(34 + 34)
	sectors := uint32(1)
	for _, c := range d.children {
		for i := 0; i < numSections(c); i++ {
			n := uint32(directoryRecordLenV(v, c))
			if used+n > LogicalSectorSize {
				sectors++
				used = 0
			}
			used += n
		}
	}
	return sectors, nil
}

// numSections reports how many Directory Records a node needs: one per File
// Section (ECMA-119 6.5.1). Directories and files small enough to fit one
// section need exactly one. This must agree with the sections actually
// allocated in fileDataSectors, so both go through sectionLengths.
func numSections(n *node) int {
	if n.isDir {
		return 1
	}
	if n.isoHidden {
		// Recorded only in UDF: the data is allocated and written, but no
		// ECMA-119 Directory Record describes it. See
		// Options.LargeFilesUDFOnly.
		return 0
	}
	if len(n.sections) > 0 {
		return len(n.sections)
	}
	return 1
}

// directoryRecordLen computes LEN_DR for a node's ECMA-119 Directory
// Record. See directoryRecordLenV.
func directoryRecordLen(n *node) int { return directoryRecordLenV(isoView, n) }

// directoryRecordLenV computes LEN_DR for a node's Directory Record in the
// given hierarchy.
//
// ECMA-119 9.1: BP 1 to 33 are fixed fields, BP 34 onwards is the File
// Identifier of length LEN_FI, and 9.1.12 adds a single (00) Padding Field
// "only if the number in the Length of the File Identifier field is an even
// number" — which keeps LEN_DR even. This package writes no System Use
// field (9.1.13), so nothing follows the padding. The rule is identical for
// the Joliet hierarchy: joliet.c's joliet_sort_n_finish computes jreclen as
// `offsetof(iso_directory_record, name[0]) + joliet_strlen(...) + 1`, the
// same "fixed part + identifier + parity byte" shape.
func directoryRecordLenV(v hierarchyView, n *node) int {
	fi := len(v.fileID(n))
	return 33 + fi + (1 - fi%2)
}

// fileIdentifier returns the File Identifier field content for a node.
//
// For a directory this is the Directory Identifier (ECMA-119 7.6, 9.1.11).
// For a file it is the full File Identifier of 7.5.1, which always ends in
// SEPARATOR 2 (';') and a File Version Number; 7.5.1 requires that number
// to be in the range 1 to 32767 and there is no way to omit it in a
// hierarchy identified by a Primary Volume Descriptor. This package always
// writes version 1, which is what every producer does.
func fileIdentifier(n *node) string {
	if n.isDir {
		return n.id
	}
	return n.id + ";1"
}

// fileDataSectors allocates an extent for every file and returns the total.
//
// A file is split into as many File Sections as its size requires, each no
// larger than Options.MaxSectionSize (ECMA-119 6.5.1; the 32-bit Data
// Length field of 9.1.4 is the reason a split is ever necessary). Splitting
// is only legal at interchange Level 3, since Levels 1 and 2 both state
// that "each file shall consist of only one File Section" (10.1, 10.2), so
// a file that would need splitting at a lower level is rejected rather than
// silently truncated.
//
// Nothing about this allocation is ISO 9660-specific, which is the point:
// UDF File Entries in a bridge volume describe these same extents, so the UDF
// layer reads node.sections rather than allocating anything of its own (see
// writeUDFFileEntries in udf.go).
func fileDataSectors(l *layout) (uint32, error) {
	base := l.currentFragmentStart()
	next := base
	for _, f := range l.files {
		lens := sectionLengths(uint64(f.size), l.b.opts.MaxSectionSize)
		if len(lens) > 1 && !f.isoHidden && l.b.opts.Level < Level3 {
			return 0, fmt.Errorf("iso: %q is %d bytes, which needs %d File Sections, "+
				"but ECMA-119 10.%d states that at this interchange level each file shall consist "+
				"of only one File Section; use Level3 or Options.LargeFilesUDFOnly",
				f.hostName, f.size, len(lens), l.b.opts.Level)
		}
		f.sections = f.sections[:0]
		for _, n := range lens {
			f.sections = append(f.sections, section{extent: next, length: n})
			next += sectorsFor(uint64(n))
		}
	}
	return next - base, nil
}

// sectionLengths splits a file size into File Section data lengths.
//
// Every section but the last is exactly max bytes long. max is a multiple
// of LogicalSectorSize (enforced in New), so each section ends on an Extent
// boundary and the next can start at the first byte of a Logical Block, as
// ECMA-119 6.4.1 requires of an Extent.
//
// A zero-length file yields a single zero-length section: 6.4.5 explicitly
// allows a File Section's data length to be zero, and a file still needs a
// Directory Record.
func sectionLengths(size uint64, max uint32) []uint32 {
	if size == 0 {
		return []uint32{0}
	}
	var out []uint32
	for size > 0 {
		n := uint64(max)
		if size < n {
			n = size
		}
		out = append(out, uint32(n))
		size -= n
	}
	return out
}

// currentFragmentStart returns the start LBA of the fragment currently
// being sized. assign records that fragment in l.cur after setting its
// start LBA and before calling its size function, so a sizing function that
// hands out extents (the directory tree and the file data) can allocate
// relative to its own base without needing to know its position in the
// list.
func (l *layout) currentFragmentStart() uint32 {
	if l.cur == nil {
		return 0
	}
	return l.cur.start
}

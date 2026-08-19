package iso

import (
	"fmt"
	"hash/fnv"
	"time"
	"unicode/utf16"
)

// This file implements the UDF side of a "bridge" volume: an image whose file
// data is described twice, once by ECMA-119 Directory Records and once by
// ECMA-167 File Entries, over one shared set of extents.
//
// # Sources
//
//   - ECMA-167 3rd edition (June 1997), "Volume and File Structure for Write
//     Once and Rewritable Media using Non-Sequential Recording for
//     Information Interchange" — the standard UDF profiles. Clause references
//     below are in the standard's own "part/clause" form, e.g. "ECMA-167
//     3/10.2" is Part 3 clause 10.2 (the Anchor Volume Descriptor Pointer)
//     and "4/14.9" is Part 4 clause 14.9 (the File Entry).
//   - OSTA Universal Disk Format Specification, revision 1.02 (August 1996).
//     This is the revision Microsoft's oscdimg -udfver102 targets and the one
//     the reference images on hand actually declare (udfinfo reports
//     udfrev=1.02 for both genisoimage's and Microsoft's Windows media), so
//     it is the revision implemented here. UDF is a *profile*: it narrows
//     ECMA-167's choices rather than replacing them, so nearly every field
//     below cites both documents.
//   - cdrkit 1.1.11, genisoimage/udf.c and genisoimage/udf_fs.h. udf.c
//     produces this project's known-good bootable Windows 11 image, which
//     makes it the highest-value cross-check available: where this file
//     deviates from it, the deviation is called out in a comment.
//
// # Why UDF is not optional for Windows media
//
// ECMA-119 9.1.4 makes a Directory Record's Data Length a 32-bit number, so
// one ISO 9660 File Section tops out just under 4 GiB. Measured on
// Win11_25H2_English_x64_v2.iso: its ISO 9660 filesystem contains exactly one
// file, a 112-byte root directory holding ".", ".." and a README.TXT that
// tells the reader it needs a UDF driver; the 7 578 075 168-byte
// sources/install.wim exists *only* in UDF. UDF is therefore the mechanism,
// not an optimisation.
//
// # Why the extents are shared rather than duplicated
//
// genisoimage's write_udf_file_entries() builds each File Entry's allocation
// descriptor from read_733(de->isorec.extent) — literally the extent the ISO
// 9660 allocator already handed out — minus the partition's starting
// location. There is one allocator, one pass and one set of extents; UDF only
// converts an absolute Logical Sector Number into a partition-relative
// Logical Block Number. Verified end-to-end on nano11go_test10.iso:
// /sources/install.wim has ISO extent 326740 and UDF short_ad position 326483
// against a partition start of 257, and 326483 + 257 = 326740.
//
// This package does the same thing: udfLayout stores only *metadata*
// locations, and every allocation descriptor is derived from node.sections,
// which fileDataSectors in layout.go filled in for the ISO 9660 layer.

// Descriptor Tag Identifiers (ECMA-167 3/7.2.1 figure 3/3 for types 1 to 9,
// 4/7.2 figure 4/3 for types 256 onwards).
const (
	udfTagPrimaryVolumeDesc          = 1   // 3/10.1
	udfTagAnchorVolumeDescPtr        = 2   // 3/10.2
	udfTagImplUseVolumeDesc          = 4   // 3/10.4
	udfTagPartitionDesc              = 5   // 3/10.5
	udfTagLogicalVolumeDesc          = 6   // 3/10.6
	udfTagUnallocatedSpaceDesc       = 7   // 3/10.8
	udfTagTerminatingDesc            = 8   // 3/10.9 and 4/14.2
	udfTagLogicalVolumeIntegrityDesc = 9   // 3/10.10
	udfTagFileSetDesc                = 256 // 4/14.1
	udfTagFileIdentDesc              = 257 // 4/14.4
	udfTagFileEntry                  = 261 // 4/14.9
)

// udfMainSeqSectors is the length of each Volume Descriptor Sequence.
//
// UDF 1.02 2.2.3.1 requires the extents pointed at by an Anchor Volume
// Descriptor Pointer to be "a minimum of 16 logical blocks in length". The
// six descriptors this package writes fit in six sectors; the remaining ten
// are left zero, exactly as genisoimage's UDF_MAIN_SEQ_LENGTH does.
const udfMainSeqSectors = 16

// udfIntegritySeqSectors is the length of the Logical Volume Integrity
// Sequence: a Logical Volume Integrity Descriptor (ECMA-167 3/10.10) followed
// by a Terminating Descriptor (3/10.9), which 3/8.8.2 requires to end the
// sequence.
const udfIntegritySeqSectors = 2

// udfAnchorSector is where the first Anchor Volume Descriptor Pointer goes.
//
// ECMA-167 3/8.4.2.1 permits anchor points at sector 256, at sector N-256 and
// at sector N (the last recorded sector), and UDF 1.02 2.2.3 narrows that to
// "shall only be recorded at 2 of the following 3 locations". Measured on
// real media: neither genisoimage nor oscdimg writes one at N-256 — that
// would punch a hole in the middle of file data — so both use 256 and N, and
// so does this package.
//
// This is the constraint that forces the whole layout: sectors up to 256 must
// be free for UDF, which is why the ISO 9660 path tables and directory tree
// move behind the UDF metadata when UDF is enabled. genisoimage's own comment
// in udf.c: "Most of the space before sector 256 on the disc (~480K) is
// wasted, because UDF Bridge requires a pointer block at sector 256."
const udfAnchorSector = 256

// udfVolumeDescriptorSequenceSector is where the two Volume Descriptor
// Sequences begin. Nothing normative pins this: it is genisoimage's choice
// (udf_pad_to_sector_32_frag) and is kept so the two producers' images line
// up sector for sector. Microsoft's oscdimg instead puts the sequences after
// the anchor, at 257 and 275 — measured with udfinfo on
// Win11_25H2_English_x64_v2.iso — which is equally legal.
const udfVolumeDescriptorSequenceSector = 32

// udfMaxExtentLength is the largest length this package puts in one
// allocation descriptor.
//
// ECMA-167 4/14.14.1.1 gives a short_ad's Extent Length field 30 bits, the
// top 2 bits being the extent type, so the ceiling is 2^30-1. UDF 1.02 2.3.10
// additionally requires the length of an extent that is not the last one of a
// file to be an integral multiple of the Logical Block Size, so the usable
// ceiling is 2^30 rounded down to a whole number of blocks: 0x3FFFF800.
// genisoimage uses exactly this constant in set_file_entry.
const udfMaxExtentLength = 0x3FFFF800

// udfFileEntryAllocDescOffset is the byte offset of the first allocation
// descriptor within a File Entry, i.e. the size of the fixed part of
// ECMA-167 4/14.9 figure 4/17.
const udfFileEntryAllocDescOffset = 176

// ICB Tag values (ECMA-167 4/14.6).
const (
	udfStrategyType4 = 4 // 4/14.6.2; UDF 1.02 2.3.5.1 allows only 4 or 4096

	udfFileTypeDirectory = 4 // 4/14.6.6
	udfFileTypeBytes     = 5 // 4/14.6.6

	// 4/14.6.8 Flags. Bits 0-2 select the allocation descriptor type; 0 means
	// short_ad (4/14.14.1), which is what this package records.
	udfICBFlagShortAD        = 0
	udfICBFlagNonRelocatable = 1 << 4
	udfICBFlagArchive        = 1 << 5
	udfICBFlagContiguous     = 1 << 9
)

// File Entry Permissions (ECMA-167 4/14.9.5). Write, Delete and Change
// Attribute are deliberately not granted: the volume is read-only media and
// UDF 1.02 2.3.6.2 notes those bits should be clear for such media.
const (
	udfPermOtherExecute = 1 << 0
	udfPermOtherRead    = 1 << 2
	udfPermGroupExecute = 1 << 5
	udfPermGroupRead    = 1 << 7
	udfPermOwnerExecute = 1 << 10
	udfPermOwnerRead    = 1 << 12

	udfPermDirectory = udfPermOtherRead | udfPermOtherExecute |
		udfPermGroupRead | udfPermGroupExecute |
		udfPermOwnerRead | udfPermOwnerExecute
	udfPermFile = udfPermOtherRead | udfPermGroupRead | udfPermOwnerRead
)

// File Characteristics bits of a File Identifier Descriptor (ECMA-167
// 4/14.4.3 figure 4/13).
const (
	udfFileCharDirectory = 1 << 1
	udfFileCharParent    = 1 << 3
)

// udfLayout holds the UDF-specific results of the sizing pass.
//
// Every field is a Logical Sector Number in the image, absolute unless the
// name says otherwise. They are filled in by the size functions in layout.go
// and consumed by the write functions here, which is safe for the same reason
// the ISO 9660 side is: sizing runs over the whole fragment list before any
// byte is written.
type udfLayout struct {
	// mainSeq and reserveSeq are the first sectors of the Main and Reserve
	// Volume Descriptor Sequences (ECMA-167 3/8.4.2), both named by the
	// Anchor Volume Descriptor Pointer.
	mainSeq    uint32
	reserveSeq uint32
	// integritySeq is the first sector of the Logical Volume Integrity
	// Sequence (3/8.8.2), named by the Logical Volume Descriptor.
	integritySeq uint32
	// partitionStart is the Partition Starting Location (3/10.5.8). Every
	// tag location and every allocation descriptor inside the partition is
	// relative to it.
	partitionStart uint32
	// endAnchor is the sector holding the second Anchor Volume Descriptor
	// Pointer, which must be the last recorded sector or one of the trailing
	// run-out sectors.
	endAnchor uint32
	// lastFileEntry is the highest sector holding a File Entry, used to seed
	// the Logical Volume Integrity Descriptor's next Unique ID.
	lastFileEntry uint32

	// dirs and files are the tree in the order their UDF metadata is
	// allocated. They are separate from layout.dirs/layout.files because the
	// ISO 9660 path table imposes a breadth-first order (ECMA-119 6.9.1)
	// that UDF has no reason to follow; this is genisoimage's depth-first
	// order (assign_udf_directory_addresses, assign_udf_file_entry_addresses).
	dirs  []*node
	files []*node
}

// udfWalk returns the tree in genisoimage's UDF allocation order: every
// directory in pre-order depth-first, and then, in that same directory order,
// the non-directory children of each.
//
// Directories come first as a block because a directory's File Entry and its
// File Identifier Descriptors must be contiguous (the File Entry's single
// allocation descriptor points at the block immediately after itself), and
// keeping all of them together is what lets the per-file File Entries be a
// simple one-sector-each run.
func udfWalk(root *node) (dirs, files []*node) {
	var walk func(*node)
	walk = func(d *node) {
		dirs = append(dirs, d)
		for _, c := range d.children {
			if c.isDir {
				walk(c)
			}
		}
	}
	walk(root)
	for _, d := range dirs {
		for _, c := range d.children {
			if !c.isDir {
				files = append(files, c)
			}
		}
	}
	return dirs, files
}

/**************** primitive encodings ****************/

// UDF records every multi-byte number little-endian: ECMA-167 1/7.1.3 to
// 1/7.1.7 define both byte orders and leave the choice to the medium, and
// UDF 1.02 2.1.7 fixes it by requiring the Volume Structure Descriptor
// "NSR02", whose Part 1 numeric encoding is least-significant-byte first.
// (This is unlike ECMA-119, which records most numbers in both orders.)

func putU16(b []byte, v uint16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}

func putU32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

func putU64(b []byte, v uint64) {
	putU32(b[0:4], uint32(v))
	putU32(b[4:8], uint32(v>>32))
}

// crcITUT computes the 16-bit CRC ECMA-167 3/7.2.6 requires for a Descriptor
// Tag: "generated by the CRC-ITU-T polynomial (see ITU-T V.41) x^16+x^12+x^5+1".
//
// The register starts at zero and no final inversion is applied. The clause's
// own worked example is used as a unit test: "the CRC of the three bytes #70
// #6A #77 is #3299".
func crcITUT(p []byte) uint16 {
	var r uint16
	for _, c := range p {
		r ^= uint16(c) << 8
		for i := 0; i < 8; i++ {
			if r&0x8000 != 0 {
				r = r<<1 ^ 0x1021
			} else {
				r <<= 1
			}
		}
	}
	return r
}

// putTag writes the 16-byte Descriptor Tag of ECMA-167 3/7.2 at the start of
// desc, over a descriptor whose total recorded length is descLen bytes.
//
// It must be called last, after every other byte of the descriptor is in
// place, because the CRC covers them.
//
//   - Descriptor Version is 2. 3/7.2.2 allows 2 or 3 and says originating
//     systems shall record 3, but that clause is about ECMA-167 3rd edition
//     structures ("NSR03"); UDF 1.02 2.2.1 requires 2, and this is a UDF
//     1.02 volume declaring NSR02. genisoimage records 2, Microsoft records 2.
//   - Tag Serial Number is 0, which 3/7.2.5 defines as "no such
//     identification is specified".
//   - location is the Tag Location of 3/7.2.8, "the number of the logical
//     sector containing the first byte of the descriptor" — but for the
//     Part 4 descriptors recorded inside a partition (File Set Descriptor,
//     File Identifier Descriptor, File Entry) 4/7.1 makes addresses
//     partition-relative, so callers pass a Logical Block Number there.
func putTag(desc []byte, tagID uint16, location uint32, descLen int) {
	putU16(desc[0:2], tagID)
	putU16(desc[2:4], 2)
	desc[4] = 0 // Tag Checksum, computed below
	desc[5] = 0 // Reserved (3/7.2.4)
	putU16(desc[6:8], 0)
	putU16(desc[8:10], crcITUT(desc[16:descLen]))
	putU16(desc[10:12], uint16(descLen-16))
	putU32(desc[12:16], location)

	// 3/7.2.3: "the sum modulo 256 of bytes 0-3 and 5-15 of the tag".
	var sum byte
	for i := 0; i < 16; i++ {
		if i == 4 {
			continue
		}
		sum += desc[i]
	}
	desc[4] = sum
}

// ostaCS0 encodes s in the OSTA Compressed Unicode format of UDF 1.02 2.1.1:
// a one-byte Compression ID followed by the characters, either one byte each
// (ID 8) or two big-endian bytes each (ID 16).
//
// The 8-bit form is used whenever every character fits, which is the common
// case for filenames on Windows media and halves the space each identifier
// costs.
func ostaCS0(s string) []byte {
	if s == "" {
		return nil
	}
	units := utf16.Encode([]rune(s))
	wide := false
	for _, u := range units {
		if u > 0xFF {
			wide = true
			break
		}
	}
	if !wide {
		out := make([]byte, 1+len(units))
		out[0] = 8
		for i, u := range units {
			out[i+1] = byte(u)
		}
		return out
	}
	out := make([]byte, 1+2*len(units))
	out[0] = 16
	for i, u := range units {
		out[1+2*i] = byte(u >> 8)
		out[2+2*i] = byte(u)
	}
	return out
}

// putDString writes s into the fixed-length dstring field dst (ECMA-167
// 1/7.2.12, restated relative to byte 0 in UDF 1.02 2.1.3): the encoded
// characters start at byte 0, the number of bytes used is recorded in the
// last byte of the field, and everything between is (00).
//
// A string too long for the field is truncated on a character boundary rather
// than rejected: these are cosmetic volume labels, and a half-written UTF-16
// code unit would be worse than a shortened label.
//
// The empty string is recorded as an all-zero field, which UDF 1.02 2.1.3
// requires explicitly ("A zero length string shall be recorded by setting the
// entire dstring field to all zeros"). This is a deliberate divergence from
// genisoimage, whose set_ostaunicode writes a lone Compression ID byte and a
// length of 1 for an empty string.
func putDString(dst []byte, s string) {
	putZero(dst)
	enc := ostaCS0(s)
	if len(enc) == 0 {
		return
	}
	max := len(dst) - 1
	if len(enc) > max {
		if enc[0] == 16 {
			// Keep a whole number of 16-bit characters.
			enc = enc[:1+(max-1)/2*2]
		} else {
			enc = enc[:max]
		}
	}
	copy(dst, enc)
	dst[len(dst)-1] = byte(len(enc))
}

// putCharspec writes the 64-byte charspec of ECMA-167 1/7.2.1 identifying the
// character set of the fields it governs. UDF 1.02 2.1.2 fixes it: type 0,
// and the information field holding the bytes "OSTA Compressed Unicode" with
// the remainder zero.
func putCharspec(dst []byte) {
	putZero(dst[:64])
	dst[0] = 0
	copy(dst[1:64], "OSTA Compressed Unicode")
}

// putEntityID writes the 32-byte regid of ECMA-167 1/7.4: a flags byte, a
// 23-byte identifier and an 8-byte identifier suffix.
//
// UDF 1.02 2.1.5 divides these into Domain, UDF and Implementation
// identifiers and specifies the suffix of each; a suffix of all zeros is what
// 2.1.5.3 defines for an Implementation Identifier whose operating system
// class is undefined, which is what genisoimage records too.
func putEntityID(dst []byte, flags byte, ident string, suffix []byte) {
	putZero(dst[:32])
	dst[0] = flags
	copy(dst[1:24], ident)
	copy(dst[24:32], suffix)
}

// udfImplIdent is this package's Implementation Identifier (UDF 1.02 2.1.5.2:
// "The first character of the Identifier field shall be #2A", i.e. '*', for
// an identifier not registered with OSTA). genisoimage records
// "*genisoimage"; Microsoft records "*Microsoft CDIMAGE UDF".
const udfImplIdent = "*gowim"

// putImplIdent writes this package's Implementation Identifier.
func putImplIdent(dst []byte) { putEntityID(dst, 0, udfImplIdent, nil) }

// putDomainIdent writes the Domain Identifier that declares the volume to
// conform to UDF (UDF 1.02 2.1.5.3): identifier "*OSTA UDF Compliant", and a
// suffix holding the UDF revision as a Uint16 (0x0102) followed by the Domain
// Flags byte. The flags are left zero, i.e. neither Hard nor Soft Write
// Protect is asserted.
//
// genisoimage writes the suffix bytes 02 01 03, i.e. revision 0x0102 with
// both write-protect flags set, which is why udfinfo reports
// softwriteprotect=yes hardwriteprotect=yes for its images and =no for
// Microsoft's. Microsoft's choice is followed here: the flags are advisory
// hints to a writing implementation and asserting them on media that is
// physically read-only conveys nothing.
func putDomainIdent(dst []byte) {
	putEntityID(dst, 0, "*OSTA UDF Compliant", []byte{0x02, 0x01, 0x00})
}

// putUDFTimestamp writes the 12-byte timestamp of ECMA-167 1/7.3.
//
// UDF 1.02 2.1.4.1 requires the Type nibble to be 1 ("Local Time"), and the
// low 12 bits to be the offset from UTC in minutes as a two's complement
// number. Go's time.Time carries a location, so the offset is taken from it
// rather than assumed.
func putUDFTimestamp(dst []byte, t time.Time) {
	_, offsetSeconds := t.Zone()
	offset := offsetSeconds / 60
	if offset < -1440 || offset > 1440 {
		// 1/7.3.1 reserves -2047 for "not specified".
		offset = -2047
	}
	putU16(dst[0:2], uint16(0x1000|(offset&0x0FFF)))
	putU16(dst[2:4], uint16(t.Year()))
	dst[4] = byte(t.Month())
	dst[5] = byte(t.Day())
	dst[6] = byte(t.Hour())
	dst[7] = byte(t.Minute())
	dst[8] = byte(t.Second())
	ns := t.Nanosecond()
	dst[9] = byte(ns / 10000000)      // centiseconds
	dst[10] = byte(ns / 100000 % 100) // hundreds of microseconds
	dst[11] = byte(ns / 1000 % 100)   // microseconds
}

// putExtentAD writes the 8-byte extent_ad of ECMA-167 3/7.1: a length in
// bytes followed by an absolute logical sector number.
func putExtentAD(dst []byte, lengthBytes uint32, location uint32) {
	putU32(dst[0:4], lengthBytes)
	putU32(dst[4:8], location)
}

// putLongAD writes the 16-byte long_ad of ECMA-167 4/14.14.2: a length, an
// lb_addr (4/7.1: a partition-relative block number plus a partition
// reference number) and six bytes of implementation use.
//
// The implementation use field is given the layout UDF 1.02 2.3.4.3 assigns
// it inside a File Identifier Descriptor — a Uint16 of flags followed by a
// Uint32 Unique ID — which is what genisoimage records and what lets a reader
// find a file's Unique ID without following the ICB.
func putLongAD(dst []byte, lengthBytes, block uint32, partition uint16, uniqueID uint32) {
	putU32(dst[0:4], lengthBytes)
	putU32(dst[4:8], block)
	putU16(dst[8:10], partition)
	putU16(dst[10:12], 0)
	putU32(dst[12:16], uniqueID)
}

/**************** sizing ****************/

// udfFIDLen returns the recorded length of a File Identifier Descriptor for
// name, or of the parent-directory descriptor when name is "".
//
// ECMA-167 4/14.4 figure 4/12 gives 38 fixed bytes plus L_IU bytes of
// implementation use (zero here) plus L_FI bytes of File Identifier, and
// 4/14.4.9 pads the total to a multiple of 4.
//
// The identifier is encoded in OSTA CS0, so L_FI is one Compression ID byte
// plus the characters. ECMA-167 4/14.4.4 makes L_FI a Uint8, and UDF 1.02
// 2.3.4 additionally caps a whole descriptor at one Logical Block; the Uint8
// is the binding limit at 255 bytes, so a name that does not fit is an error
// rather than a silent truncation, which would risk colliding with a sibling.
func udfFIDLen(name string) (int, error) {
	lenFI := len(ostaCS0(name))
	if lenFI > 255 {
		return 0, fmt.Errorf("iso: %q encodes to %d bytes in OSTA CS0, but the Length of File "+
			"Identifier field of ECMA-167 4/14.4.4 is an 8-bit number", name, lenFI)
	}
	n := 38 + lenFI
	return (n + 3) &^ 3, nil
}

// udfDirectoryBytes returns the total size of a directory's File Identifier
// Descriptors: one for the parent (ECMA-167 4/8.6, and UDF 1.02 3.3.1 which
// requires it to be recorded first) followed by one per child.
//
// Files hidden from the ISO 9660 layer are still listed here — that is the
// whole point of the bridge — so this walks node.children rather than
// anything ISO-specific.
func udfDirectoryBytes(d *node) (uint32, error) {
	total, err := udfFIDLen("")
	if err != nil {
		return 0, err
	}
	for _, c := range d.children {
		n, err := udfFIDLen(c.hostName)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return uint32(total), nil
}

// udfDirTreeSectors sizes the UDF directory region and assigns each directory
// its File Entry sector.
//
// Each directory costs one sector for its File Entry plus however many blocks
// its File Identifier Descriptors need, and the two are adjacent because the
// File Entry's single allocation descriptor points at the block immediately
// after itself (genisoimage's write_one_udf_directory does the same).
func (l *layout) udfDirTreeSectors() (uint32, error) {
	base := l.currentFragmentStart()
	next := base
	for _, d := range l.udf.dirs {
		n, err := udfDirectoryBytes(d)
		if err != nil {
			return 0, err
		}
		d.udfEntry = next
		d.udfDirBytes = n
		next += 1 + sectorsFor(uint64(n))
	}
	return next - base, nil
}

// udfFileEntriesSectors assigns one sector per file for its File Entry.
//
// UDF has no way to pack several File Entries into a block: ECMA-167 4/14.9
// descriptors are addressed by block, and UDF 1.02 2.3.6 caps one at a
// Logical Block. genisoimage's header comment puts the cost plainly: "there
// is an overhead of more than 2K per file when using UDF". On the reference
// image that is 1 164 sectors of metadata for roughly 1 045 nodes.
func (l *layout) udfFileEntriesSectors() (uint32, error) {
	base := l.currentFragmentStart()
	next := base
	for _, f := range l.udf.files {
		if _, err := udfAllocDescCount(f); err != nil {
			return 0, err
		}
		f.udfEntry = next
		next++
	}
	if next > base {
		l.udf.lastFileEntry = next - 1
	}
	return next - base, nil
}

// udfAllocDescCount returns how many short_ads a file's File Entry needs, and
// rejects a file that would need more than fit in one block.
func udfAllocDescCount(f *node) (int, error) {
	n := 1
	if f.size > 0 {
		n = int((uint64(f.size) + udfMaxExtentLength - 1) / udfMaxExtentLength)
	}
	if max := (LogicalSectorSize - udfFileEntryAllocDescOffset) / 8; n > max {
		return 0, fmt.Errorf("iso: %q is %d bytes, needing %d allocation descriptors, but a File "+
			"Entry holds at most %d (UDF 1.02 2.3.6 caps a File Entry at one Logical Block); "+
			"recording it would need an Allocation Extent Descriptor, which is not implemented",
			f.hostName, f.size, n, max)
	}
	return n, nil
}

/**************** writing ****************/

// writeUDFVolumeRecognitionSequence emits the three Volume Structure
// Descriptors that tell a reader the volume carries an ECMA-167 filesystem
// (ECMA-167 2/9.1, and 2/9.2 for the identifiers).
//
//   - "BEA01": Beginning Extended Area Descriptor (2/9.2).
//   - "NSR02": the ECMA-167/2 filesystem structures live here (3/9.1). NSR02
//     rather than NSR03 because this is a UDF 1.02 volume; UDF 2.00 and later
//     use NSR03.
//   - "TEA01": Terminating Extended Area Descriptor (2/9.3).
//
// ECMA-167 2/8.3.1 Note 1 makes the placement load-bearing: the recognition
// sequence ends at the first sector that is not a valid Volume Structure
// Descriptor, so these must follow the ECMA-119 descriptor set with no gap.
// That is why this fragment sits immediately after the Volume Descriptor Set
// Terminator in buildLayout.
func (l *layout) writeUDFVolumeRecognitionSequence(w *sectorWriter) error {
	for _, id := range []string{"BEA01", "NSR02", "TEA01"} {
		s := w.sector()
		s[0] = 0 // 2/9.1.1 Structure Type
		copy(s[1:6], id)
		s[6] = 1 // 2/9.1.3 Structure Version
		if err := w.write(s); err != nil {
			return err
		}
	}
	return nil
}

// writeUDFMainSeq emits one Volume Descriptor Sequence (ECMA-167 3/8.4.2).
//
// The Main and Reserve sequences hold the same information but are separate
// recordings: every descriptor's Tag Location names the sector it actually
// occupies, so the Reserve copy has to be regenerated rather than memcpy'd
// from the Main one. That is why this takes the base sector as an argument.
//
// The sequence ends with a Terminating Descriptor (3/10.9), which 3/8.4.2
// requires, and the remainder of the 16 reserved sectors is left zero.
func (l *layout) writeUDFMainSeq(w *sectorWriter, base uint32) error {
	writers := []func([]byte, uint32){
		l.setPrimaryVolumeDesc,
		l.setImplUseVolumeDesc,
		l.setPartitionDesc,
		l.setLogicalVolumeDesc,
		l.setUnallocatedSpaceDesc,
		setTerminatingDesc,
	}
	for i, f := range writers {
		s := w.sector()
		f(s, base+uint32(i))
		if err := w.write(s); err != nil {
			return err
		}
	}
	return w.zeroSectors(udfMainSeqSectors - uint32(len(writers)))
}

// volumeSetIdent builds the Volume Set Identifier (ECMA-167 3/10.1.5).
//
// UDF 1.02 2.2.2.5 requires the first 16 characters to be unique, and
// recommends the first 8 be hexadecimal digits so that the identifier can be
// used to distinguish volume sets. genisoimage uses the build time and
// clock(), which makes its output non-reproducible; this package hashes the
// volume identifier and the caller-supplied timestamp instead, so that
// building the same tree twice produces the same image.
func (l *layout) volumeSetIdent() string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00%d", l.b.opts.VolumeID, l.b.opts.Timestamp.UnixNano())
	return fmt.Sprintf("%016X%s", h.Sum64(), l.b.opts.VolumeID)
}

// setPrimaryVolumeDesc writes the UDF Primary Volume Descriptor (ECMA-167
// 3/10.1). It describes the *physical* volume; the Logical Volume Descriptor
// below describes the filesystem. Note this is a completely different
// structure from the ECMA-119 Primary Volume Descriptor at sector 16, which
// happens to share the name.
func (l *layout) setPrimaryVolumeDesc(s []byte, lba uint32) {
	o := l.b.opts
	putU32(s[16:20], 0) // 3/10.1.2 Volume Descriptor Sequence Number
	putU32(s[20:24], 0) // 3/10.1.3 Primary Volume Descriptor Number
	putDString(s[24:56], o.VolumeID)
	putU16(s[56:58], 1) // 3/10.1.5 Volume Sequence Number
	putU16(s[58:60], 1) // 3/10.1.6 Maximum Volume Sequence Number
	putU16(s[60:62], 2) // 3/10.1.7 Interchange Level; UDF 1.02 2.2.2.4 requires 2
	putU16(s[62:64], 2) // 3/10.1.8 Maximum Interchange Level
	putU32(s[64:68], 1) // 3/10.1.9 Character Set List: bit 0 = CS0
	putU32(s[68:72], 1) // 3/10.1.10 Maximum Character Set List
	putDString(s[72:200], l.volumeSetIdent())
	putCharspec(s[200:264]) // 3/10.1.12 Descriptor Character Set
	putCharspec(s[264:328]) // 3/10.1.13 Explanatory Character Set
	// 3/10.1.14 Volume Abstract and 3/10.1.15 Volume Copyright Notice: zero
	// length means no such extent is recorded.
	putUDFTimestamp(s[376:388], o.Timestamp) // 3/10.1.17 Recording Date and Time
	putImplIdent(s[388:420])                 // 3/10.1.18 Implementation Identifier
	putTag(s, udfTagPrimaryVolumeDesc, lba, 512)
}

// setImplUseVolumeDesc writes the Implementation Use Volume Descriptor
// (ECMA-167 3/10.4), whose content UDF 1.02 2.2.7 defines as the "UDF LV
// Info" structure: the logical volume identifier repeated, three free-form
// information strings, and the implementation identifier.
//
// It exists so that a reader can display a volume's name and the tool that
// wrote it without mounting the filesystem, and udfinfo's owner=,
// organization= and contact= fields come from exactly these three strings.
func (l *layout) setImplUseVolumeDesc(s []byte, lba uint32) {
	putU32(s[16:20], 1) // Volume Descriptor Sequence Number
	// 3/10.4.3 Implementation Identifier, fixed by UDF 1.02 2.2.7 to
	// "*UDF LV Info" with the UDF revision in the suffix.
	putEntityID(s[20:52], 0, "*UDF LV Info", []byte{0x02, 0x01})
	putCharspec(s[52:116]) // UDF 1.02 2.2.7.1 LVICharset
	putDString(s[116:244], l.b.opts.VolumeID)
	// LVInfo1/2/3 (UDF 1.02 2.2.7.2) are left as zero-length dstrings.
	putDString(s[244:280], "")
	putDString(s[280:316], "")
	putDString(s[316:352], "")
	putImplIdent(s[352:384])
	putTag(s, udfTagImplUseVolumeDesc, lba, 512)
}

// setPartitionDesc writes the Partition Descriptor (ECMA-167 3/10.5).
//
// This is the descriptor that turns absolute sector numbers into the
// partition-relative block numbers every Part 4 structure uses, so it is the
// hinge of the whole shared-extent scheme: a file's UDF allocation descriptor
// is its ISO 9660 extent minus Partition Starting Location.
//
// The partition is declared to run from its start through the end anchor
// inclusive. genisoimage computes the same value whenever it pads (its
// default), because udf_padend_avdp_size resets lba_end_anchor_vol_desc to
// the sector *after* the single end anchor; measured on nano11go_test10.iso,
// udfinfo reports start=257 blocks=1848690 with the end anchor at 1848946,
// and 257+1848690 = 1848947 = 1848946+1.
func (l *layout) setPartitionDesc(s []byte, lba uint32) {
	u := l.udf
	putU32(s[16:20], 2) // Volume Descriptor Sequence Number
	// 3/10.5.3 Partition Flags bit 0 "Allocated": the volume space for this
	// partition is allocated.
	putU16(s[20:22], 1)
	putU16(s[22:24], 0) // 3/10.5.4 Partition Number
	// 3/10.5.5 Partition Contents. UDF 1.02 2.2.14.2 requires "+NSR02" for a
	// partition holding ECMA-167 Part 4 structures. The Protected flag
	// (1/7.4.1) marks the identifier as not to be reused, which is what
	// genisoimage sets.
	putEntityID(s[24:56], 2, "+NSR02", nil)
	putU32(s[184:188], 1) // 3/10.5.7 Access Type: 1 = read-only
	putU32(s[188:192], u.partitionStart)
	putU32(s[192:196], u.endAnchor+1-u.partitionStart)
	putImplIdent(s[196:228])
	putTag(s, udfTagPartitionDesc, lba, 512)
}

// setLogicalVolumeDesc writes the Logical Volume Descriptor (ECMA-167
// 3/10.6), which names the filesystem, fixes the Logical Block Size, points
// at the File Set Descriptor and maps logical blocks onto the partition.
func (l *layout) setLogicalVolumeDesc(s []byte, lba uint32) {
	u := l.udf
	putU32(s[16:20], 3) // Volume Descriptor Sequence Number
	putCharspec(s[20:84])
	putDString(s[84:212], l.b.opts.VolumeID)
	// 3/10.6.5 Logical Block Size. UDF 1.02 2.2.4.2 requires it to equal the
	// logical sector size of the media, which for a CD/DVD image is 2048.
	putU32(s[212:216], LogicalSectorSize)
	putDomainIdent(s[216:248])
	// 3/10.6.7 Logical Volume Contents Use, which 4/3.1 defines as a long_ad
	// locating the File Set Descriptor sequence: two blocks (the descriptor
	// and its Terminating Descriptor) at the start of partition 0.
	putLongAD(s[248:264], 2*LogicalSectorSize, 0, 0, 0)
	putU32(s[264:268], 6) // 3/10.6.8 Map Table Length
	putU32(s[268:272], 1) // 3/10.6.9 Number of Partition Maps
	putImplIdent(s[272:304])
	// 3/10.6.12 Integrity Sequence Extent.
	putExtentAD(s[432:440], udfIntegritySeqSectors*LogicalSectorSize, u.integritySeq)
	// 3/10.7.2 Type 1 Partition Map: a plain one-to-one mapping of logical
	// blocks onto the partition's blocks, which UDF 1.02 2.2.8 requires for
	// non-rewritable media.
	s[440] = 1 // Partition Map Type
	s[441] = 6 // Partition Map Length
	putU16(s[442:444], 1)
	putU16(s[444:446], 0)
	putTag(s, udfTagLogicalVolumeDesc, lba, 446)
}

// setUnallocatedSpaceDesc writes the Unallocated Space Descriptor (ECMA-167
// 3/10.8) with no extents: the whole volume is allocated, which is what a
// read-only image means. UDF 1.02 2.2.5 requires exactly one such descriptor
// per volume even when it is empty.
func (l *layout) setUnallocatedSpaceDesc(s []byte, lba uint32) {
	putU32(s[16:20], 4) // Volume Descriptor Sequence Number
	putU32(s[20:24], 0) // Number of Allocation Descriptors
	putTag(s, udfTagUnallocatedSpaceDesc, lba, 24)
}

// setTerminatingDesc writes a Terminating Descriptor (ECMA-167 3/10.9,
// identical to 4/14.2), which marks the end of a descriptor sequence.
func setTerminatingDesc(s []byte, lba uint32) {
	putTag(s, udfTagTerminatingDesc, lba, 512)
}

// writeUDFIntegritySeq emits the Logical Volume Integrity Sequence: one
// Logical Volume Integrity Descriptor (ECMA-167 3/10.10) followed by a
// Terminating Descriptor.
//
// The LVID is what tells a reader the volume is consistent and does not need
// repair, and it carries the file and directory counts and the UDF revision
// that udfinfo reports. UDF 1.02 2.2.6.4 requires the Integrity Type of the
// last LVID on read-only media to be 1, "close".
func (l *layout) writeUDFIntegritySeq(w *sectorWriter) error {
	u := l.udf
	s := w.sector()
	putUDFTimestamp(s[16:28], l.b.opts.Timestamp) // 3/10.10.2 Recording Date and Time
	putU32(s[28:32], 1)                           // 3/10.10.3 Integrity Type: close
	// 3/10.10.4 Next Integrity Extent: zero length, no continuation.
	// 3/10.10.6 Logical Volume Contents Use, which 4/3.1 defines for an LVID
	// as the Unique ID to hand out next. Every Unique ID this package assigns
	// is a File Entry's sector number, so one past the last File Entry is
	// guaranteed unused.
	putU64(s[40:48], uint64(u.lastFileEntry)+1)
	putU32(s[72:76], 1)  // 3/10.10.7 Number of Partitions
	putU32(s[76:80], 46) // 3/10.10.8 Length of Implementation Use
	// 3/10.10.9 Free Space Table: 0, no free space on read-only media.
	putU32(s[80:84], 0)
	// 3/10.10.10 Size Table: the partition's length in blocks.
	putU32(s[84:88], u.endAnchor+1-u.partitionStart)
	// 3/10.10.11 Implementation Use, whose layout UDF 1.02 2.2.6.4 defines.
	putImplIdent(s[88:120])
	putU32(s[120:124], uint32(len(u.files)))
	putU32(s[124:128], uint32(len(u.dirs)))
	putU16(s[128:130], 0x0102) // Minimum UDF Read Revision
	putU16(s[130:132], 0x0102) // Minimum UDF Write Revision
	putU16(s[132:134], 0x0102) // Maximum UDF Write Revision
	putTag(s, udfTagLogicalVolumeIntegrityDesc, u.integritySeq, 88+46)
	if err := w.write(s); err != nil {
		return err
	}

	s = w.sector()
	setTerminatingDesc(s, u.integritySeq+1)
	return w.write(s)
}

// writeUDFAnchor emits one Anchor Volume Descriptor Pointer (ECMA-167
// 3/10.2) at the given sector.
//
// The anchor is the entry point: a reader looks at sector 256, finds the two
// Volume Descriptor Sequences from it, and works down from there. Because the
// descriptor is self-locating (its Tag Location holds its own sector number),
// every copy has to be built separately — which is confirmed by measurement
// on nano11go_test10.iso, where each of the 151 trailing anchors carries its
// own sector number.
func (l *layout) writeUDFAnchor(w *sectorWriter, lba uint32) error {
	s := w.sector()
	putExtentAD(s[16:24], udfMainSeqSectors*LogicalSectorSize, l.udf.mainSeq)
	putExtentAD(s[24:32], udfMainSeqSectors*LogicalSectorSize, l.udf.reserveSeq)
	putTag(s, udfTagAnchorVolumeDescPtr, lba, 512)
	return w.write(s)
}

// writeUDFFileSetDesc emits the File Set Descriptor (ECMA-167 4/14.1) and its
// Terminating Descriptor at the start of the partition.
//
// This is the first Part 4 structure, so it is the first whose Tag Location is
// partition-relative (4/7.1): the descriptor at absolute sector
// partitionStart records location 0.
func (l *layout) writeUDFFileSetDesc(w *sectorWriter) error {
	u := l.b.opts
	root := l.udf.dirs[0]

	s := w.sector()
	putUDFTimestamp(s[16:28], u.Timestamp) // 4/14.1.2 Recording Date and Time
	putU16(s[28:30], 3)                    // 4/14.1.3 Interchange Level; UDF 1.02 2.3.2.1 requires 3
	putU16(s[30:32], 3)                    // 4/14.1.4 Maximum Interchange Level
	putU32(s[32:36], 1)                    // 4/14.1.5 Character Set List
	putU32(s[36:40], 1)                    // 4/14.1.6 Maximum Character Set List
	putU32(s[40:44], 0)                    // 4/14.1.7 File Set Number
	putU32(s[44:48], 0)                    // 4/14.1.8 File Set Descriptor Number
	putCharspec(s[48:112])                 // 4/14.1.9
	putDString(s[112:240], u.VolumeID)     // 4/14.1.10 Logical Volume Identifier
	putCharspec(s[240:304])                // 4/14.1.11
	putDString(s[304:336], u.VolumeID)     // 4/14.1.12 File Set Identifier
	// 4/14.1.13 Copyright File Identifier and 4/14.1.14 Abstract File
	// Identifier: zero-length, no such file.
	putDString(s[336:368], "")
	putDString(s[368:400], "")
	// 4/14.1.15 Root Directory ICB: the root directory's File Entry, as a
	// partition-relative block number.
	putLongAD(s[400:416], LogicalSectorSize, root.udfEntry-l.udf.partitionStart, 0, 0)
	putDomainIdent(s[416:448]) // 4/14.1.16 Domain Identifier
	// 4/14.1.17 Next Extent: zero length, this is the only File Set
	// Descriptor, which UDF 1.02 2.3.2 requires for read-only media.
	putTag(s, udfTagFileSetDesc, 0, 512)
	if err := w.write(s); err != nil {
		return err
	}

	s = w.sector()
	setTerminatingDesc(s, 1)
	return w.write(s)
}

// setFileEntry formats a File Entry (ECMA-167 4/14.9) describing a file or
// directory whose data occupies size bytes starting at partition-relative
// block dataBlock.
//
// The single most important thing this function does *not* do is allocate:
// dataBlock comes from the extent the ISO 9660 layer already assigned. That
// is what makes the volume a bridge rather than two filesystems in a trench
// coat, and it is exactly what genisoimage's write_udf_file_entries does with
// read_733(de->isorec.extent) - lba_udf_partition_start.
func (l *layout) setFileEntry(s []byte, entryBlock, dataBlock uint32, size uint64,
	mtime time.Time, isDir bool, linkCount uint16, uniqueID uint64) error {

	// 4/14.6 ICB Tag.
	putU32(s[16:20], 0)                // Prior Recorded Number of Direct Entries
	putU16(s[20:22], udfStrategyType4) // 4/14.6.2 Strategy Type
	putU16(s[22:24], 0)                // 4/14.6.3 Strategy Parameter
	putU16(s[24:26], 1)                // 4/14.6.4 Maximum Number of Entries
	s[26] = 0                          // reserved
	if isDir {
		s[27] = udfFileTypeDirectory
	} else {
		s[27] = udfFileTypeBytes
	}
	// 4/14.6.7 Parent ICB Location: zero, meaning not specified. UDF 1.02
	// 2.3.5.5 permits this; the parent is discoverable from the parent File
	// Identifier Descriptor that 4/8.6 requires in every directory.
	putU16(s[34:36], udfICBFlagShortAD|udfICBFlagNonRelocatable|
		udfICBFlagArchive|udfICBFlagContiguous)

	// 4/14.9.2 UID and 4/14.9.3 GID. #FFFFFFFF is the value 4/14.9.2 defines
	// as "an invalid UID", i.e. no owner is recorded — appropriate for media
	// authored on a machine whose user ids mean nothing to the reader.
	putU32(s[36:40], 0xFFFFFFFF)
	putU32(s[40:44], 0xFFFFFFFF)
	if isDir {
		putU32(s[44:48], udfPermDirectory)
	} else {
		putU32(s[44:48], udfPermFile)
	}
	putU16(s[48:50], linkCount) // 4/14.9.6 File Link Count
	s[50] = 0                   // 4/14.9.7 Record Format: not specified
	s[51] = 0                   // 4/14.9.8 Record Display Attributes
	putU32(s[52:56], 0)         // 4/14.9.9 Record Length
	// 4/14.9.10 Information Length is a Uint64, which is the entire reason
	// UDF can carry the 7 GiB install.wim that ECMA-119's 32-bit Data Length
	// cannot.
	putU64(s[56:64], size)
	putU64(s[64:72], uint64(sectorsFor(size))) // 4/14.9.11 Logical Blocks Recorded
	putUDFTimestamp(s[72:84], mtime)           // 4/14.9.12 Access Time
	putUDFTimestamp(s[84:96], mtime)           // 4/14.9.13 Modification Time
	putUDFTimestamp(s[96:108], mtime)          // 4/14.9.14 Attribute Time
	putU32(s[108:112], 1)                      // 4/14.9.15 Checkpoint
	// 4/14.9.16 Extended Attribute ICB: zero, none recorded.
	putImplIdent(s[128:160])     // 4/14.9.17 Implementation Identifier
	putU64(s[160:168], uniqueID) // 4/14.9.18 Unique Id
	putU32(s[168:172], 0)        // 4/14.9.19 Length of Extended Attributes

	// 4/14.9.21 Allocation Descriptors, in the short_ad form of 4/14.14.1
	// selected by the ICB Tag flags above: a 30-bit length with a 2-bit
	// extent type in the top bits (type 0, "recorded and allocated"), and a
	// partition-relative block number.
	off := udfFileEntryAllocDescOffset
	remaining := size
	for remaining > 0 {
		chunk := remaining
		if chunk > udfMaxExtentLength {
			chunk = udfMaxExtentLength
		}
		if off+8 > LogicalSectorSize {
			return fmt.Errorf("iso: internal error: File Entry allocation descriptors overflow a block")
		}
		putU32(s[off:off+4], uint32(chunk))
		putU32(s[off+4:off+8], dataBlock)
		dataBlock += sectorsFor(chunk)
		remaining -= chunk
		off += 8
	}
	putU32(s[172:176], uint32(off-udfFileEntryAllocDescOffset)) // 4/14.9.20 L_AD

	putTag(s, udfTagFileEntry, entryBlock, off)
	return nil
}

// setFileIdentDesc formats a File Identifier Descriptor (ECMA-167 4/14.4)
// into dst and returns its recorded length.
//
// A name of "" produces the parent-directory descriptor that 4/8.6 requires
// every directory to contain and that UDF 1.02 3.3.1 requires to be recorded
// first. Such a descriptor has no File Identifier at all — it is identified
// by the Parent bit of the File Characteristics field (4/14.4.3 bit 3).
func setFileIdentDesc(dst []byte, block uint32, name string, isDir bool,
	entryBlock uint32, uniqueID uint32) (int, error) {

	total, err := udfFIDLen(name)
	if err != nil {
		return 0, err
	}
	putZero(dst[:total])

	putU16(dst[16:18], 1) // 4/14.4.2 File Version Number; UDF 1.02 2.3.4.1 requires 1
	var chars byte
	if isDir {
		chars |= udfFileCharDirectory
	}
	if name == "" {
		chars |= udfFileCharParent
	}
	dst[18] = chars
	id := ostaCS0(name)
	dst[19] = byte(len(id)) // 4/14.4.4 Length of File Identifier
	// 4/14.4.5 ICB: a long_ad naming the File Entry of the file this
	// descriptor identifies.
	putLongAD(dst[20:36], LogicalSectorSize, entryBlock, 0, uniqueID)
	putU16(dst[36:38], 0) // 4/14.4.6 Length of Implementation Use
	copy(dst[38:], id)
	// 4/14.4.9 Padding is already (00) from putZero.

	putTag(dst[:total], udfTagFileIdentDesc, block, total)
	return total, nil
}

// udfLinkCount returns a directory's File Link Count (ECMA-167 4/14.9.6): one
// for the parent directory's reference to it, plus one for each of its own
// subdirectories' parent references.
func udfLinkCount(d *node) uint16 {
	n := 1
	for _, c := range d.children {
		if c.isDir {
			n++
		}
	}
	return uint16(n)
}

// udfUniqueID returns the Unique Id (ECMA-167 4/14.9.18) for a node.
//
// UDF 1.02 3.2.1 requires the root directory's Unique Id to be 0 and every
// other one to be distinct within the logical volume. A File Entry's own
// sector number satisfies both conditions for free and needs no counter,
// which is genisoimage's trick too.
func udfUniqueID(n *node) uint64 {
	if n.parent == nil {
		return 0
	}
	return uint64(n.udfEntry)
}

// writeUDFDirTree emits every directory's File Entry followed by that
// directory's File Identifier Descriptors.
func (l *layout) writeUDFDirTree(w *sectorWriter) error {
	for _, d := range l.udf.dirs {
		if err := l.writeUDFDirectory(w, d); err != nil {
			return err
		}
	}
	return nil
}

func (l *layout) writeUDFDirectory(w *sectorWriter, d *node) error {
	base := d.udfEntry - l.udf.partitionStart

	s := w.sector()
	if err := l.setFileEntry(s, base, base+1, uint64(d.udfDirBytes), d.modTime,
		true, udfLinkCount(d), udfUniqueID(d)); err != nil {
		return err
	}
	if err := w.write(s); err != nil {
		return err
	}

	// The File Identifier Descriptors are the directory's *data*, addressed
	// by the File Entry's allocation descriptor, so they start in the block
	// after the entry. ECMA-167 imposes no rule against a descriptor
	// spanning a block boundary here — unlike ECMA-119 6.8.1.1, which
	// requires every Directory Record to end in the sector it begins in — so
	// they are packed with no padding, exactly as genisoimage does. The Tag
	// Location of each is the block holding its first byte (3/7.2.8).
	buf := make([]byte, d.udfDirBytes)
	off := 0
	parent := d.parent
	if parent == nil {
		// 4/8.6: the parent of the root directory is the root directory.
		parent = d
	}
	// The Tag Location of a descriptor is the block holding its first byte
	// (3/7.2.8), which for the data of a directory whose File Entry sits at
	// block base is base+1 plus the offset in blocks.
	tagBlock := func(off int) uint32 { return base + 1 + uint32(off/LogicalSectorSize) }

	n, err := setFileIdentDesc(buf[off:], tagBlock(off),
		"", true, parent.udfEntry-l.udf.partitionStart, uint32(udfUniqueID(parent)))
	if err != nil {
		return err
	}
	off += n
	for _, c := range d.children {
		n, err := setFileIdentDesc(buf[off:], tagBlock(off),
			c.hostName, c.isDir, c.udfEntry-l.udf.partitionStart, uint32(udfUniqueID(c)))
		if err != nil {
			return err
		}
		off += n
	}
	if uint32(off) != d.udfDirBytes {
		return fmt.Errorf("iso: internal error: UDF directory %q wrote %d bytes but was sized as %d",
			d.hostName, off, d.udfDirBytes)
	}
	if err := w.write(buf); err != nil {
		return err
	}
	return w.padToSector()
}

// writeUDFFileEntries emits one File Entry per file.
//
// The allocation descriptors are derived from node.sections, which the ISO
// 9660 layer filled in: the file's data was written exactly once and both
// filesystems point at it. Where the ISO layer split a large file into
// several File Sections, those sections were allocated back to back, so UDF
// sees a single contiguous run and re-splits it on its own 2^30-byte
// boundary rather than ECMA-119's 4 GiB one.
func (l *layout) writeUDFFileEntries(w *sectorWriter) error {
	for _, f := range l.udf.files {
		s := w.sector()
		var start uint32
		if len(f.sections) > 0 {
			start = f.sections[0].extent
		}
		if err := l.setFileEntry(s, f.udfEntry-l.udf.partitionStart,
			start-l.udf.partitionStart, uint64(f.size), f.modTime,
			false, 1, udfUniqueID(f)); err != nil {
			return err
		}
		if err := w.write(s); err != nil {
			return err
		}
	}
	return nil
}

// writeUDFTrailingAnchors fills the run-out padding with copies of the Anchor
// Volume Descriptor Pointer instead of zeros, which is what genisoimage's
// udf_padend_avdp_write does.
//
// The point is robustness: ECMA-167 3/8.4.2.1 allows an anchor at the last
// recorded sector, but a drive's idea of the last recorded sector can differ
// from the image's by a few blocks, so filling the whole run-out with
// self-locating anchors means a reader finds one wherever it looks.
func (l *layout) writeUDFTrailingAnchors(w *sectorWriter, start, count uint32) error {
	for i := uint32(0); i < count; i++ {
		if err := l.writeUDFAnchor(w, start+i); err != nil {
			return err
		}
	}
	return nil
}

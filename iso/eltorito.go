package iso

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

// El Torito bootable CD-ROM support.
//
// # Sources
//
// The normative reference is the "Bootable CD-ROM Format Specification"
// version 1.0 (Phoenix Technologies and IBM, 25 January 1995), cited below as
// "El Torito 1.0" with the figure numbers it uses for its structures. A copy
// is mirrored by MIT PDOS at
// https://pdos.csail.mit.edu/6.828/2017/readings/boot-cdrom.pdf.
//
// El Torito 1.0 predates UEFI and defines only three Platform IDs — 0 (80x86),
// 1 (PowerPC) and 2 (Mac), see its Figures 2 and 4. The value 0xEF that every
// modern UEFI installation disc uses is defined not there but in the UEFI
// specification, version 2.10 section 13.3.2.1 "ISO-9660 and El Torito",
// https://uefi.org/specs/UEFI/2.10/13_Protocols_Media_Access.html. That
// section is also where the rule about a Sector Count of 0 or 1 meaning "from
// the beginning of the image to the end of the CD-ROM" lives, which is why
// Microsoft's oscdimg can get away with writing 1 there.
//
// The cross-checkable implementation is cdrkit 1.1.11's genisoimage, files
// genisoimage/eltorito.c (get_torito_desc, fill_boot_desc) and
// genisoimage/iso9660.h (the four on-disk structures and the EL_TORITO_*
// constants), plus genisoimage/bootinfo.h for the non-standard boot
// information table. That program produces this project's known-good bootable
// Windows 11 image, so its output can be diffed against this package's.
//
// # What is written
//
// Three things, and only the first two are standard:
//
//  1. A Boot Record Volume Descriptor (ECMA-119 8.2, whose "Boot System Use"
//     field of BP 72 to 2048 El Torito 1.0 Figure 7 claims), placed
//     immediately after the Primary Volume Descriptor. It holds a 32-bit
//     little-endian pointer to the boot catalog's Logical Block Number.
//
//  2. A boot catalog: one Logical Sector holding a Validation Entry
//     (Figure 2), an Initial/Default Entry (Figure 3) for the first boot
//     image, and then for every further image a Section Header (Figure 4)
//     followed by a Section Entry (Figure 5). The catalog is an ordinary file
//     in the ISO 9660 tree, allocated by the same allocator as every other
//     file; only its *contents* are generated, after every extent is known.
//     genisoimage does exactly the same thing (eltorito.c's insert_boot_cat
//     creates the catalog as a "memory file").
//
//  3. Optionally, per entry, genisoimage's "boot information table": 56 bytes
//     written over byte offsets 8 to 63 of the boot image. This is not in any
//     specification — it is a genisoimage invention that isolinux and
//     Microsoft's etfsboot.com both read — and its field map is
//     genisoimage/bootinfo.h's struct genisoimage_boot_info.
//
// # A deliberate divergence from genisoimage
//
// genisoimage patches the boot information table into the *source file on
// disk*: eltorito.c's fill_boot_desc opens de->whole_name with O_RDWR, seeks
// to offset 8, and writes the table there; the image is then produced by
// copying the file it has just mutated. The caller's input file is silently
// modified, and since the table embeds the LBAs of the image being built, two
// runs against different trees leave two different files behind. This was
// measured on this project's own working trees: a pristine retail
// etfsboot.com hashes f425e135aac26b55..., while the two trees that
// genisoimage has been run over carry 7568bfc63beb6b52... and
// f41a9d3866880f22..., differing only inside bytes 8 to 63.
//
// This package never touches an input file. The table is inserted into the
// output byte stream as the boot image is copied (see writeFileData), so
// Sources stay read-only, a Source that is not a file at all still works, and
// the same input tree can be built twice with identical results. The emitted
// bytes are identical to genisoimage's; only the side effect is gone.
// TestBootImageSourceIsNotModified asserts the property.

// BootPlatform is the El Torito "Platform ID" byte, which appears in the
// Validation Entry (El Torito 1.0 Figure 2, offset 1) for the first boot
// entry and in a Section Header (Figure 4, offset 1) for every later one.
type BootPlatform uint8

const (
	// BootPlatformX86 is El Torito 1.0's "0 = 80x86", i.e. a legacy BIOS
	// boot image.
	BootPlatformX86 BootPlatform = 0x00
	// BootPlatformPowerPC is El Torito 1.0's "1 = Power PC".
	BootPlatformPowerPC BootPlatform = 0x01
	// BootPlatformMac is El Torito 1.0's "2 = Mac".
	BootPlatformMac BootPlatform = 0x02
	// BootPlatformUEFI is 0xEF, which El Torito 1.0 does not define at all.
	// It comes from UEFI 2.10 section 13.3.2.1: "the Platform ID ... 0xEF
	// indicates a UEFI System Partition". This is the value on every
	// UEFI-bootable Windows installation disc, and the one genisoimage names
	// EL_TORITO_ARCH_EFI in iso9660.h.
	BootPlatformUEFI BootPlatform = 0xEF
)

// BootEntry describes one El Torito boot image.
//
// Every entry this package writes is a "no emulation" entry, i.e. Boot Media
// Type 0 of El Torito 1.0 Figures 3 and 5. That is what Windows installation
// media uses on both of its entries, what genisoimage's -no-emul-boot
// selects, and what oscdimg's "e" flag in -bootdata:...#p0,e,b... means.
// Floppy and hard-disk emulation are not implemented: they would need this
// package to parse an MBR out of the boot image and to reason about emulated
// geometry, neither of which has any use here, and shipping an untested
// implementation of them would be worse than not having one.
type BootEntry struct {
	// ImagePath is the path of the boot image *within the image being
	// built*, in the caller's own (unmangled) names, e.g.
	// "boot/etfsboot.com". The file must already have been added to the
	// Builder; it is recorded once and both the ISO 9660 directory record and
	// this catalog entry point at the same extent.
	ImagePath string

	// Platform is the El Torito Platform ID. The zero value is
	// BootPlatformX86.
	Platform BootPlatform

	// LoadSegment is the real-mode segment the BIOS loads the image at
	// (Figure 3 offset 2-3). El Torito 1.0: "If this value is 0 the system
	// will use the traditional segment of 7C0", which is what every producer
	// writes and what the zero value here means. x86 only.
	LoadSegment uint16

	// SystemType is Figure 3's offset 4, "a copy of byte 5 (System Type) from
	// the Partition Table found in the boot image". It is meaningful only for
	// hard-disk emulation, which this package does not write, so it is 0 in
	// practice. Real media agrees: it is 0 on both entries of this project's
	// reference image and of Microsoft's own.
	SystemType byte

	// LoadSectors is the Sector Count of Figure 3 offset 6-7: "the number of
	// virtual/emulated sectors the system will store at Load Segment", in
	// 512-byte units, not 2048-byte Logical Sectors.
	//
	// Zero means "derive it from the file's size", which is what genisoimage
	// does when -boot-load-size is absent: eltorito.c's fill_boot_desc
	// computes ISO_BLOCKS(de->size) * (SECTOR_SIZE/512), i.e. the size rounded
	// up to a whole Logical Sector and then expressed in 512-byte units. For
	// the 1 474 560-byte efisys_noprompt.bin that yields 2880, the value
	// measured in the reference image.
	//
	// The BIOS entry of Windows media instead uses an explicit 8
	// (genisoimage -boot-load-size 8, oscdimg's default), meaning only the
	// first 4 KiB of etfsboot.com is loaded at boot time.
	//
	// A UEFI entry may also set this to 1 to invoke UEFI 2.10 section
	// 13.3.2.1's rule that a Sector Count of 0 or 1 means the system
	// partition runs "from the beginning of the 'no emulation' image to the
	// end of the CD-ROM". That is what oscdimg writes; genisoimage writes the
	// true size. Both boot.
	LoadSectors uint16

	// NotBootable records the entry with Boot Indicator 00 rather than 88
	// (Figures 3 and 5, offset 0), i.e. present in the catalog but not
	// offered as a boot option.
	NotBootable bool

	// BootInfoTable requests genisoimage's 56-byte boot information table
	// over bytes 8 to 63 of this image, as -boot-info-table does. See the
	// file comment: unlike genisoimage, this package patches only the output
	// stream, never the caller's file.
	//
	// It is off by default because oscdimg emits no such table and Microsoft
	// ships etfsboot.com with real executable code in those bytes; it is
	// needed only to reproduce a genisoimage-built image.
	BootInfoTable bool
}

const (
	// elToritoID is the Boot System Identifier of the Boot Record Volume
	// Descriptor: El Torito 1.0 Figure 7, offset 7-26 (hex), "must be
	// 'EL TORITO SPECIFICATION' padded with 0's". Note the padding is (00),
	// not the (20) FILLER that ECMA-119 a-character fields use.
	elToritoID = "EL TORITO SPECIFICATION"

	// bootIndicatorBootable and bootIndicatorNotBootable are Figure 3 and
	// Figure 5 offset 0: "88 = Bootable, 00 = Not Bootable".
	bootIndicatorBootable    = 0x88
	bootIndicatorNotBootable = 0x00

	// bootMediaNoEmulation is Boot Media Type 0, "No Emulation" (Figures 3
	// and 5, offset 1, bits 0-3).
	bootMediaNoEmulation = 0x00

	// bootSectionHeaderMore and bootSectionHeaderFinal are Figure 4 offset 0:
	// "90 - Header, more headers follow / 91 - Final Header".
	bootSectionHeaderMore  = 0x90
	bootSectionHeaderFinal = 0x91

	// bootCatalogEntrySize is the size of every catalog structure: the
	// Validation Entry, the Initial/Default Entry, a Section Header and a
	// Section Entry are all 32 bytes (El Torito 1.0 Figures 2 to 5, which all
	// run from offset 0 to 1F).
	bootCatalogEntrySize = 32

	// defaultBootCatalogPath is where the catalog is recorded when
	// Options.BootCatalogPath is empty. genisoimage's default is the same
	// name (genisoimage.c: boot_catalog defaults to BOOT_CATALOG_DEFAULT,
	// "boot.catalog"), and the reference image records it at /boot.catalog.
	defaultBootCatalogPath = "boot.catalog"

	// bootInfoTableSize is sizeof(struct genisoimage_boot_info) from
	// genisoimage/bootinfo.h: four 32-bit little-endian fields followed by 40
	// reserved bytes.
	bootInfoTableSize = 56

	// bootInfoTableOffset is where that table goes. genisoimage's
	// fill_boot_desc does lseek(bootimage, (off_t)8, SEEK_SET) before writing
	// it, so it covers bytes 8 to 63 and leaves the first eight bytes — on
	// x86 typically a jump and a stack setup — alone.
	bootInfoTableOffset = 8
)

// bootLayout holds El Torito's per-image state, resolved once during layout.
type bootLayout struct {
	// catalog is the tree node of the generated boot catalog file.
	catalog *node
	// catalogSrc is that node's Source, whose bytes are filled in after
	// every extent has been assigned.
	catalogSrc *bootCatalogSource
	// images[i] is the tree node named by Options.BootEntries[i].
	images []*node
}

// bootCatalogSource is the Source backing the generated boot catalog.
//
// The catalog is exactly one Logical Sector, which is what makes this work at
// all: its size is known before layout (so the allocator can place it) while
// its contents depend on the LBAs layout hands out (so they can only be
// produced afterwards). genisoimage relies on the same fixed size —
// insert_boot_cat does set_733(s_entry->isorec.size, SECTOR_SIZE) on a
// freshly calloc'd sector.
type bootCatalogSource struct{ data []byte }

// Size reports the catalog's fixed one-sector length.
func (s *bootCatalogSource) Size() (int64, error) { return LogicalSectorSize, nil }

// Open returns the generated catalog. It is an internal error to reach here
// before layout has produced the bytes.
func (s *bootCatalogSource) Open() (io.ReadCloser, error) {
	if s.data == nil {
		return nil, errors.New("iso: internal error: the boot catalog was written before it was generated")
	}
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

// bootCatalogPath returns where the catalog is to be recorded.
func (b *Builder) bootCatalogPath() string {
	if b.opts.BootCatalogPath != "" {
		return b.opts.BootCatalogPath
	}
	return defaultBootCatalogPath
}

// addBootCatalog inserts the generated boot catalog into the tree.
//
// It runs before finalize so that the catalog takes part in identifier
// mangling, collision resolution and 9.3 ordering exactly like any other
// file: it is a real file in the recorded hierarchy, not a hidden region.
func (b *Builder) addBootCatalog() error {
	src := &bootCatalogSource{}
	if err := b.AddFile(b.bootCatalogPath(), src); err != nil {
		return fmt.Errorf("iso: recording the El Torito boot catalog: %w", err)
	}
	b.bootCatalogSrc = src
	return nil
}

// findNode resolves a slash-separated path of caller-supplied (unmangled)
// names to a tree node, or nil.
func (b *Builder) findNode(p string) *node {
	cur := b.root
	for _, part := range splitPath(p) {
		if cur == nil || !cur.isDir {
			return nil
		}
		cur = cur.find(part)
	}
	if cur == b.root {
		return nil
	}
	return cur
}

// initBoot resolves every boot entry against the tree and validates it.
//
// This runs during layout construction, before any sector is written, so that
// a mistyped boot image path is reported immediately rather than after several
// gigabytes have been streamed out.
func (l *layout) initBoot() error {
	b := l.b
	bl := &bootLayout{catalogSrc: b.bootCatalogSrc}
	bl.catalog = b.findNode(b.bootCatalogPath())
	if bl.catalog == nil {
		return errors.New("iso: internal error: the boot catalog is not in the tree")
	}

	// El Torito 1.0 section 2.1 requires the Validation Entry to be the
	// first entry in the catalog, and the catalog is one sector, so the
	// structures must fit: 32 bytes for the Validation Entry, 32 for the
	// Initial/Default Entry, and 64 for each further entry (a Section Header
	// plus its Section Entry). genisoimage checks the same bound with its
	// repeated "Too many El Torito boot entries" test against SECTOR_SIZE.
	need := 2 * bootCatalogEntrySize
	if n := len(b.opts.BootEntries); n > 1 {
		need += (n - 1) * 2 * bootCatalogEntrySize
	}
	if need > LogicalSectorSize {
		return fmt.Errorf("iso: %d El Torito boot entries need %d bytes, but the boot catalog is one "+
			"%d-byte Logical Sector", len(b.opts.BootEntries), need, LogicalSectorSize)
	}

	for i, e := range b.opts.BootEntries {
		n := b.findNode(e.ImagePath)
		if n == nil {
			return fmt.Errorf("iso: El Torito boot entry %d names %q, which is not in the image",
				i+1, e.ImagePath)
		}
		if n.isDir {
			return fmt.Errorf("iso: El Torito boot entry %d names %q, which is a directory",
				i+1, e.ImagePath)
		}
		if n.size == 0 {
			return fmt.Errorf("iso: El Torito boot entry %d names %q, which is empty", i+1, e.ImagePath)
		}
		if e.BootInfoTable && n.size < bootInfoTableOffset+bootInfoTableSize {
			return fmt.Errorf("iso: El Torito boot entry %d asks for a boot information table in %q, "+
				"but the file is only %d bytes and the table occupies bytes %d to %d",
				i+1, e.ImagePath, n.size, bootInfoTableOffset, bootInfoTableOffset+bootInfoTableSize-1)
		}
		bl.images = append(bl.images, n)
	}
	l.boot = bl
	return nil
}

// finishBoot generates the boot catalog and any boot information tables. It
// runs after the sizing pass, when every extent is final, and before the
// first byte of the image is written.
func (l *layout) finishBoot() error {
	buf := make([]byte, LogicalSectorSize)
	off := 0

	// El Torito 1.0 Figure 2, the Validation Entry. Its Platform ID is that
	// of the *first* boot entry; entries after the first carry their own in a
	// Section Header. genisoimage does the same
	// (valid_desc.arch[0] = first_boot_entry->arch).
	v := buf[off : off+bootCatalogEntrySize]
	put711(v[0:1], 1)                                       // offset 0: Header ID, must be 01
	put711(v[1:2], uint8(l.b.opts.BootEntries[0].Platform)) // offset 1: Platform ID
	putZero(v[2:4])                                         // offset 2-3: Reserved, must be 0
	putStrZeroPad(v[4:28], l.b.opts.BootCatalogID)          // offset 4-1B: ID string
	putZero(v[28:30])                                       // offset 1C-1D: Checksum Word, zero while summing
	v[30] = 0x55                                            // offset 1E: Key byte, must be 55
	v[31] = 0xAA                                            // offset 1F: Key byte, must be AA
	put721(v[28:30], bootValidationChecksum(v))
	off += bootCatalogEntrySize

	for i, e := range l.b.opts.BootEntries {
		if i > 0 {
			// El Torito 1.0 Figure 4, a Section Header per further platform.
			// One entry per header, which is what genisoimage emits
			// unconditionally (set_721(section_header.num_entries, 1)) and
			// what -eltorito-alt-boot produces: that option writes nothing
			// itself, it only starts a new entry in genisoimage's list.
			h := buf[off : off+bootCatalogEntrySize]
			id := byte(bootSectionHeaderMore)
			if i == len(l.b.opts.BootEntries)-1 {
				id = bootSectionHeaderFinal
			}
			put711(h[0:1], id)                // offset 0: Header Indicator
			put711(h[1:2], uint8(e.Platform)) // offset 1: Platform ID
			put721(h[2:4], 1)                 // offset 2-3: number of section entries
			putZero(h[4:32])                  // offset 4-1F: ID string
			off += bootCatalogEntrySize
		}

		// El Torito 1.0 Figure 3 (Initial/Default Entry) for i == 0 and
		// Figure 5 (Section Entry) otherwise. The two are laid out
		// identically through offset 0B; they differ only in what offsets 0C
		// to 1F mean (Figure 3 "Unused, must be 0"; Figure 5 a selection
		// criteria type plus vendor-unique bytes). Writing zeros there is
		// correct for both, and gives Figure 5's "0 - No selection criteria".
		// genisoimage likewise formats both with one function, fill_boot_desc.
		n := l.boot.images[i]
		sectors, err := bootSectorCount(e, n)
		if err != nil {
			return fmt.Errorf("iso: El Torito boot entry %d (%q): %w", i+1, e.ImagePath, err)
		}
		if len(n.sections) != 1 {
			return fmt.Errorf("iso: El Torito boot entry %d (%q) is recorded as %d File Sections; "+
				"a boot image must be one contiguous extent because the catalog records a single "+
				"Load RBA", i+1, e.ImagePath, len(n.sections))
		}
		d := buf[off : off+bootCatalogEntrySize]
		if e.NotBootable {
			put711(d[0:1], bootIndicatorNotBootable)
		} else {
			put711(d[0:1], bootIndicatorBootable)
		}
		put711(d[1:2], bootMediaNoEmulation)  // offset 1: Boot media type
		put721(d[2:4], e.LoadSegment)         // offset 2-3: Load Segment
		put711(d[4:5], e.SystemType)          // offset 4: System Type
		putZero(d[5:6])                       // offset 5: Unused, must be 0
		put721(d[6:8], sectors)               // offset 6-7: Sector Count
		put731(d[8:12], n.sections[0].extent) // offset 8-0B: Load RBA
		putZero(d[12:32])
		off += bootCatalogEntrySize

		if e.BootInfoTable {
			tbl, err := l.bootInfoTable(n)
			if err != nil {
				return fmt.Errorf("iso: El Torito boot entry %d (%q): %w", i+1, e.ImagePath, err)
			}
			n.bootInfoTable = tbl
		}
	}

	l.boot.catalogSrc.data = buf
	return nil
}

// bootValidationChecksum returns the value for the Checksum Word of the
// Validation Entry.
//
// El Torito 1.0 Figure 2 says only "This sum of all the words in this record
// should be 0" — it states neither a word width, nor a byte order, nor a
// modulus. The implementation fact, verified numerically against both
// genisoimage's and oscdimg's output, is: interpret the 32-byte entry as
// sixteen little-endian 16-bit words, key bytes 55 AA included, and the sum
// must be zero modulo 2^16. genisoimage computes exactly that in
// get_torito_desc, accumulating checksum_ptr[i] + checksum_ptr[i+1]*256 and
// then storing -checksum.
//
// v must already have zeros in the checksum word.
func bootValidationChecksum(v []byte) uint16 {
	var sum uint16
	for i := 0; i+1 < len(v); i += 2 {
		sum += uint16(v[i]) | uint16(v[i+1])<<8
	}
	return -sum
}

// bootSectorCount returns the Sector Count field for an entry, in 512-byte
// units. See BootEntry.LoadSectors for where the derived value comes from.
func bootSectorCount(e BootEntry, n *node) (uint16, error) {
	if e.LoadSectors != 0 {
		return e.LoadSectors, nil
	}
	v := uint64(sectorsFor(uint64(n.size))) * (LogicalSectorSize / 512)
	if v > 0xFFFF {
		return 0, fmt.Errorf("the image is %d bytes, which is %d 512-byte sectors, but El Torito's "+
			"Sector Count field is a 16-bit word; set BootEntry.LoadSectors explicitly", n.size, v)
	}
	return uint16(v), nil
}

// bootInfoTable builds genisoimage's 56-byte boot information table for a
// boot image, per genisoimage/bootinfo.h's struct genisoimage_boot_info: the
// LBA of the Primary Volume Descriptor, the LBA of the boot image, the boot
// image's length in bytes, a checksum of the image, and 40 reserved bytes,
// the first four fields being 32-bit little-endian (a "731" number in
// ECMA-119 7.3.1 terms).
func (l *layout) bootInfoTable(n *node) ([]byte, error) {
	sum, err := bootImageChecksum(n)
	if err != nil {
		return nil, err
	}
	if uint64(n.size) > 0xFFFFFFFF {
		return nil, fmt.Errorf("the image is %d bytes, which does not fit the table's 32-bit "+
			"length field", n.size)
	}
	tbl := make([]byte, bootInfoTableSize)
	// genisoimage writes session_start + 16 here, session_start being 0 for a
	// single-session image. firstVolumeDescriptorSector is that same 16, and
	// this package writes the Primary Volume Descriptor there.
	put731(tbl[0:4], firstVolumeDescriptorSector)
	put731(tbl[4:8], n.sections[0].extent)
	put731(tbl[8:12], uint32(n.size))
	put731(tbl[12:16], sum)
	putZero(tbl[16:])
	return tbl, nil
}

// bootImageChecksum reproduces genisoimage's boot image checksum exactly.
//
// From fill_boot_desc: the file is read in 2048-byte chunks; in the first
// chunk the leading 64 bytes are zeroed ("if (total_len < 64) memset(...)"),
// the final short chunk is zero-filled out to 2048, and every chunk
// contributes the sum of its 512 little-endian 32-bit words. The sum is taken
// modulo 2^32.
//
// Zeroing the first 64 bytes is what makes the scheme self-consistent: those
// are precisely the bytes the table itself overwrites, so the checksum is the
// same whether it is computed before or after the table is inserted. That in
// turn is why this package can compute it from the untouched Source and still
// match genisoimage, which computes it from a file it is about to modify.
func bootImageChecksum(n *node) (uint32, error) {
	rc, err := n.src.Open()
	if err != nil {
		return 0, fmt.Errorf("opening %q to checksum it: %w", n.hostName, err)
	}
	defer rc.Close()

	var (
		sum   uint32
		total int64
		buf   = make([]byte, LogicalSectorSize)
	)
	for {
		k, err := io.ReadFull(rc, buf)
		if k > 0 {
			if total == 0 {
				// The first 64 bytes are excluded from the sum.
				putZero(buf[:min(k, bootInfoTableOffset+bootInfoTableSize)])
			}
			putZero(buf[k:])
			for i := 0; i < LogicalSectorSize; i += 4 {
				sum += uint32(buf[i]) | uint32(buf[i+1])<<8 |
					uint32(buf[i+2])<<16 | uint32(buf[i+3])<<24
			}
			total += int64(k)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("reading %q to checksum it: %w", n.hostName, err)
		}
	}
	if total != n.size {
		return 0, fmt.Errorf("%q is %d bytes but reported %d during layout", n.hostName, total, n.size)
	}
	return sum, nil
}

// writeBootRecord emits the Boot Record Volume Descriptor.
//
// ECMA-119 8.2 defines the descriptor generically — a Volume Descriptor Type
// of 0, the Standard Identifier, a version, a Boot System Identifier and a
// Boot Identifier, then a "Boot System Use" field from BP 72 to 2048 "reserved
// for system use ... not specified by this Standard". El Torito 1.0 Figure 7
// specifies that field: the Boot System Identifier must be
// "EL TORITO SPECIFICATION" padded with zeros, BP 40 to 71 must be zero, and
// BP 72 to 75 hold an absolute pointer to the first sector of the boot
// catalog. The pointer is a plain 32-bit little-endian number, not one of
// ECMA-119 7.3's both-byte-order forms.
func (l *layout) writeBootRecord(w *sectorWriter) error {
	s := w.sector()
	put711(s[0:1], 0)                  // 8.2.1 Volume Descriptor Type: 0 = Boot Record
	copy(s[1:6], "CD001")              // 8.2.2 Standard Identifier
	put711(s[6:7], 1)                  // 8.2.3 Volume Descriptor Version
	putStrZeroPad(s[7:39], elToritoID) // 8.2.4 Boot System Identifier (BP 8-39)
	putZero(s[39:71])                  // 8.2.5 Boot Identifier (BP 40-71)
	put731(s[71:75], l.boot.catalog.sections[0].extent)
	putZero(s[75:2048])
	return w.write(s)
}

// applyBootInfoTable returns a reader over a boot image with the boot
// information table spliced over bytes 8 to 63.
//
// The length is unchanged — 56 bytes are replaced by 56 bytes — so the extent
// the layout pass allocated still holds, and nothing downstream shifts. The
// caller's Source is only read.
func applyBootInfoTable(r io.Reader, tbl []byte) (io.Reader, error) {
	head := make([]byte, bootInfoTableOffset+bootInfoTableSize)
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, fmt.Errorf("reading the first %d bytes of the boot image: %w", len(head), err)
	}
	copy(head[bootInfoTableOffset:], tbl)
	return io.MultiReader(bytes.NewReader(head), r), nil
}

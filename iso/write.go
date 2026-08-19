package iso

import (
	"errors"
	"fmt"
	"io"
	"time"
)

// sectorWriter is a sequential writer that tracks how many bytes have been
// emitted, so that each fragment can be checked against the length it
// claimed during the sizing pass.
//
// Writing is strictly sequential and forward-only: the image is produced in
// one pass with no seeking, which is what makes it practical to write a
// multi-gigabyte Windows image straight to a pipe or to a file on a
// nearly-full disc. This is the reason layout must be complete before any
// byte is written — nothing can be patched up afterwards.
type sectorWriter struct {
	w   io.Writer
	n   int64
	buf []byte // one scratch sector, reused
}

func newSectorWriter(w io.Writer) *sectorWriter {
	return &sectorWriter{w: w, buf: make([]byte, LogicalSectorSize)}
}

// sector returns the scratch sector, zeroed, for a caller to fill in.
func (w *sectorWriter) sector() []byte {
	putZero(w.buf)
	return w.buf
}

func (w *sectorWriter) write(p []byte) error {
	n, err := w.w.Write(p)
	w.n += int64(n)
	return err
}

// zeroSectors emits n zero-filled Logical Sectors.
func (w *sectorWriter) zeroSectors(n uint32) error {
	putZero(w.buf)
	for i := uint32(0); i < n; i++ {
		if err := w.write(w.buf); err != nil {
			return err
		}
	}
	return nil
}

// padToSector emits (00) bytes until the output is sector-aligned. ECMA-119
// 6.8.1.1 requires the unused byte positions after the last Directory
// Record in a Logical Sector to be set to (00); the same zero fill is used
// for the tail of the path tables and of each file's last sector, where the
// standard does not specify the content but a deterministic image is worth
// having.
func (w *sectorWriter) padToSector() error {
	rem := w.n % LogicalSectorSize
	if rem == 0 {
		return nil
	}
	putZero(w.buf)
	return w.write(w.buf[:LogicalSectorSize-rem])
}

// WriteTo lays out the image and writes it to w, returning the number of
// bytes written.
//
// The image is produced in two passes. The first assigns every fragment,
// directory and file extent a Logical Sector Number; the second serialises
// the fragments in order. File contents are read during the second pass
// only, so the peak memory cost is one sector plus a copy buffer regardless
// of image size.
func (b *Builder) WriteTo(w io.Writer) (int64, error) {
	if b.err != nil {
		return 0, b.err
	}
	if err := b.finalize(); err != nil {
		return 0, err
	}
	l := buildLayout(b)
	if err := l.assign(); err != nil {
		return 0, err
	}
	sw := newSectorWriter(w)
	for _, f := range l.frags {
		before := sw.n
		if err := f.write(l, sw); err != nil {
			return sw.n, fmt.Errorf("iso: writing %s: %w", f.name, err)
		}
		want := int64(f.sectors) * LogicalSectorSize
		if got := sw.n - before; got != want {
			return sw.n, fmt.Errorf("iso: internal error: fragment %s wrote %d bytes but was laid out as %d",
				f.name, got, want)
		}
	}
	return sw.n, nil
}

// writePVD emits the Primary Volume Descriptor (ECMA-119 8.4).
//
// Field offsets below are the byte positions of 8.4 Table 4, converted from
// the standard's 1-based BP numbering to 0-based Go slice indices.
func (l *layout) writePVD(w *sectorWriter) error {
	o := l.b.opts
	s := w.sector()

	put711(s[0:1], 1)     // 8.4.1 Volume Descriptor Type: 1 = Primary
	copy(s[1:6], "CD001") // 8.4.2 Standard Identifier
	put711(s[6:7], 1)     // 8.4.3 Volume Descriptor Version
	putZero(s[7:8])       // 8.4.4 Unused Field
	putStrPad(s[8:40], sanitizeAChars(o.SystemID))
	putStrPad(s[40:72], sanitizeDChars(o.VolumeID))
	putZero(s[72:80])                     // 8.4.7 Unused Field
	put733(s[80:88], l.totalSectors)      // 8.4.8 Volume Space Size, in Logical Blocks
	putZero(s[88:120])                    // 8.4.9 Unused Field (Joliet reuses it as Escape Sequences, 8.5.6)
	put723(s[120:124], 1)                 // 8.4.10 Volume Set Size
	put723(s[124:128], 1)                 // 8.4.11 Volume Sequence Number
	put723(s[128:132], LogicalSectorSize) // 8.4.12 Logical Block Size
	put733(s[132:140], l.pathTableSize)   // 8.4.13 Path Table Size, in bytes

	// 8.4.14 to 8.4.17. The "Optional Occurrence" fields are set to zero,
	// which 8.4.15 and 8.4.17 define as meaning no such table is recorded.
	// Note the asymmetry the standard imposes: the Type L locations are
	// recorded per 7.3.1 (LSB first) and the Type M locations per 7.3.2
	// (MSB first), matching the byte order of the table each points at.
	put731(s[140:144], l.fragStart("Type L Path Table"))
	put731(s[144:148], 0)
	put732(s[148:152], l.fragStart("Type M Path Table"))
	put732(s[152:156], 0)

	// 8.4.18 Directory Record for Root Directory: a complete 34-byte
	// Directory Record, embedded in the descriptor.
	if err := writeDirectoryRecord(s[156:190], l.dirs[0], selfRecord, l.b.opts); err != nil {
		return err
	}

	putStrPad(s[190:318], sanitizeDChars(o.VolumeSetID))   // 8.4.19
	putStrPad(s[318:446], sanitizeAChars(o.PublisherID))   // 8.4.20
	putStrPad(s[446:574], sanitizeAChars(o.PreparerID))    // 8.4.21
	putStrPad(s[574:702], sanitizeAChars(o.ApplicationID)) // 8.4.22

	// 8.4.23 to 8.4.25 name files in the root directory that hold the
	// copyright notice, abstract and bibliography. This package records
	// none, and the clauses define an all-FILLER field as meaning no such
	// file is identified.
	putStrPad(s[702:739], "")
	putStrPad(s[739:776], "")
	putStrPad(s[776:813], "")

	putLongDateTime(s[813:830], o.Timestamp) // 8.4.26 Creation
	putLongDateTime(s[830:847], o.Timestamp) // 8.4.27 Modification
	putLongDateTime(s[847:864], time.Time{}) // 8.4.28 Expiration: not specified
	putLongDateTime(s[864:881], o.Timestamp) // 8.4.29 Effective

	// 8.4.30 File Structure Version: "For a Primary Volume Descriptor ...
	// 1 shall indicate the structure of this Standard." (Value 2 is for an
	// Enhanced Volume Descriptor, which this package does not write yet.)
	put711(s[881:882], 1)
	putZero(s[882:883])   // 8.4.31
	putZero(s[883:1395])  // 8.4.32 Application Use, left empty
	putZero(s[1395:2048]) // 8.4.33

	return w.write(s)
}

// writeTerminator emits a Volume Descriptor Set Terminator (ECMA-119 8.3).
// 8.3 requires the descriptor set to be terminated by at least one of
// these; one is enough.
func (l *layout) writeTerminator(w *sectorWriter) error {
	s := w.sector()
	put711(s[0:1], 255)   // 8.3.1 Volume Descriptor Type
	copy(s[1:6], "CD001") // 8.3.2 Standard Identifier
	put711(s[6:7], 1)     // 8.3.3 Volume Descriptor Version
	putZero(s[7:2048])    // 8.3.4 Reserved, all (00)
	return w.write(s)
}

// fragStart returns the start LBA of the named fragment. Fragment names are
// internal constants, so a miss is a programming error.
func (l *layout) fragStart(name string) uint32 {
	for _, f := range l.frags {
		if f.name == name {
			return f.start
		}
	}
	panic("iso: no fragment named " + name)
}

// writePathTable emits one Path Table (ECMA-119 9.4), in Type M byte order
// if msb is set and Type L order otherwise (6.9.2).
//
// The two tables are byte-for-byte identical apart from the byte order of
// their two numeric fields, so they are generated by the same code. The
// record order is the order of l.dirs, which node.dirs already produced to
// satisfy 6.9.1.
func (l *layout) writePathTable(w *sectorWriter, msb bool) error {
	buf := make([]byte, 0, l.pathTableSize)
	for _, d := range l.dirs {
		id := pathTableID(d)
		rec := make([]byte, pathTableRecordLen(d))
		put711(rec[0:1], uint8(len(id))) // 9.4.1 Length of Directory Identifier
		put711(rec[1:2], 0)              // 9.4.2 Extended Attribute Record length
		if msb {
			put732(rec[2:6], d.dirExtent)    // 9.4.3 Location of Extent
			put722(rec[6:8], parentIndex(d)) // 9.4.4 Parent Directory Number
		} else {
			put731(rec[2:6], d.dirExtent)
			put721(rec[6:8], parentIndex(d))
		}
		copy(rec[8:], id) // 9.4.5 Directory Identifier
		// 9.4.6 Padding Field: present only when LEN_DI is odd, and the
		// slice was sized to include it, so the tail is already (00).
		buf = append(buf, rec...)
	}
	if err := w.write(buf); err != nil {
		return err
	}
	return w.padToSector()
}

// parentIndex returns the Path Table record number of a directory's parent
// (ECMA-119 9.4.4). 6.8.2 states that "The Parent Directory of the Root
// Directory shall be the Root Directory", so the root's record points at
// itself, i.e. at record number 1.
func parentIndex(d *node) uint16 {
	if d.parent == nil {
		return 1
	}
	return d.parent.pathIndex
}

// recordKind selects which of the three flavours of Directory Record to
// emit for a node.
type recordKind int

const (
	// childRecord is an ordinary record naming a child of the directory it
	// appears in.
	childRecord recordKind = iota
	// selfRecord is the "." record: ECMA-119 6.8.2.2 requires the first
	// Directory Record of a directory to describe the directory itself,
	// with a Directory Identifier consisting of a single (00) byte.
	selfRecord
	// parentRecord is the ".." record: 6.8.2.2 requires the second record
	// to describe the Parent Directory, with a single (01) byte.
	parentRecord
)

// writeDirectoryRecord formats one Directory Record (ECMA-119 9.1) into
// dst, which must be exactly LEN_DR bytes long.
func writeDirectoryRecord(dst []byte, n *node, kind recordKind, opts Options) error {
	var (
		id     string
		extent uint32
		length uint32
		flags  byte
		mtime  time.Time
	)
	switch kind {
	case selfRecord:
		id, extent, length, flags, mtime = "\x00", n.dirExtent, n.dirLength, dirFlag, n.modTime
	case parentRecord:
		p := n.parent
		if p == nil {
			// 6.8.2: the Parent Directory of the Root Directory is the
			// Root Directory, so the root's ".." record points at the
			// root itself.
			p = n
		}
		id, extent, length, flags, mtime = "\x01", p.dirExtent, p.dirLength, dirFlag, p.modTime
	default:
		id, mtime = fileIdentifier(n), n.modTime
		if n.isDir {
			extent, length, flags = n.dirExtent, n.dirLength, dirFlag
		} else {
			return errors.New("iso: internal error: file records are emitted per File Section")
		}
	}
	return putDirectoryRecord(dst, id, extent, length, flags, mtime, opts)
}

// dirFlag is bit 1 of the File Flags field (ECMA-119 9.1.6 Table 10,
// "Directory"): set to ONE means the Directory Record identifies a
// directory.
const dirFlag = 0x02

// multiExtentFlag is bit 7 of the File Flags field (ECMA-119 9.1.6
// Table 10, "Multi-Extent"): "If set to ONE, shall mean that this is not
// the final Directory Record for the file." It is how a file recorded as
// several File Sections is stitched back together, and it is the only
// standard-conformant way to record a file larger than the 32-bit Data
// Length field of 9.1.4 permits.
const multiExtentFlag = 0x80

func putDirectoryRecord(dst []byte, id string, extent, length uint32, flags byte, mtime time.Time, opts Options) error {
	fi := len(id)
	lenDR := 33 + fi + (1 - fi%2)
	if len(dst) != lenDR {
		return fmt.Errorf("iso: internal error: directory record buffer is %d bytes, need %d", len(dst), lenDR)
	}
	if lenDR > 255 {
		return fmt.Errorf("iso: identifier %q makes a %d-byte Directory Record, "+
			"but the Length of Directory Record field of ECMA-119 9.1.1 is an 8-bit number", id, lenDR)
	}
	putZero(dst)
	put711(dst[0:1], uint8(lenDR)) // 9.1.1 LEN_DR
	put711(dst[1:2], 0)            // 9.1.2 Extended Attribute Record Length
	put733(dst[2:10], extent)      // 9.1.3 Location of Extent
	put733(dst[10:18], length)     // 9.1.4 Data Length
	putShortDateTime(dst[18:25], mtime)
	dst[25] = flags               // 9.1.6 File Flags
	put711(dst[26:27], 0)         // 9.1.7 File Unit Size: 0, not interleaved (6.4.4)
	put711(dst[27:28], 0)         // 9.1.8 Interleave Gap Size: 0, not interleaved
	put723(dst[28:32], 1)         // 9.1.9 Volume Sequence Number
	put711(dst[32:33], uint8(fi)) // 9.1.10 LEN_FI
	copy(dst[33:33+fi], id)       // 9.1.11 File Identifier
	// 9.1.12 Padding Field is already (00) from putZero.
	_ = opts
	return nil
}

// writeDirectories emits every directory's extent, in the order the sizing
// pass allocated them.
func (l *layout) writeDirectories(w *sectorWriter) error {
	for _, d := range l.dirs {
		if err := l.writeDirectory(w, d); err != nil {
			return err
		}
	}
	return nil
}

func (l *layout) writeDirectory(w *sectorWriter, d *node) error {
	buf := make([]byte, 0, d.dirLength)

	// 6.8.2.2: the first two records are "." and "..", in that order.
	self := make([]byte, 34)
	if err := writeDirectoryRecord(self, d, selfRecord, l.b.opts); err != nil {
		return err
	}
	parent := make([]byte, 34)
	if err := writeDirectoryRecord(parent, d, parentRecord, l.b.opts); err != nil {
		return err
	}
	buf = append(buf, self...)
	buf = append(buf, parent...)

	for _, c := range d.children {
		if c.isDir {
			rec := make([]byte, directoryRecordLen(c))
			if err := writeDirectoryRecord(rec, c, childRecord, l.b.opts); err != nil {
				return err
			}
			buf = appendRecord(buf, rec)
			continue
		}
		// One Directory Record per File Section (6.5.1). Every record but
		// the last carries the Multi-Extent flag (9.1.6 bit 7), and 9.2
		// requires the File Identifier and the non-Multi-Extent File Flags
		// bits to be identical across all of them.
		for i, sec := range c.sections {
			flags := byte(0)
			if i != len(c.sections)-1 {
				flags |= multiExtentFlag
			}
			rec := make([]byte, directoryRecordLen(c))
			if err := putDirectoryRecord(rec, fileIdentifier(c), sec.extent, sec.length, flags, c.modTime, l.b.opts); err != nil {
				return err
			}
			buf = appendRecord(buf, rec)
		}
	}

	if uint32(len(buf)) > d.dirLength {
		return fmt.Errorf("iso: internal error: directory %q needs %d bytes but was laid out as %d",
			d.hostName, len(buf), d.dirLength)
	}
	if err := w.write(buf); err != nil {
		return err
	}
	// 6.8.1.3: the directory's recorded length includes the unused bytes
	// after the last record in every sector it occupies, so pad out to the
	// laid-out length rather than merely to a sector boundary.
	return w.zeroBytes(int(d.dirLength) - len(buf))
}

// appendRecord appends a Directory Record to a directory extent under
// ECMA-119 6.8.1.1: "Each Directory Record shall end in the Logical Sector
// in which it begins", and unused positions after the last record in a
// sector are set to (00). A record that will not fit in the remainder of
// the current sector therefore starts the next one.
func appendRecord(buf, rec []byte) []byte {
	used := len(buf) % LogicalSectorSize
	if used+len(rec) > LogicalSectorSize {
		buf = append(buf, make([]byte, LogicalSectorSize-used)...)
	}
	return append(buf, rec...)
}

// zeroBytes emits n zero bytes.
func (w *sectorWriter) zeroBytes(n int) error {
	if n < 0 {
		return errors.New("iso: internal error: negative pad")
	}
	putZero(w.buf)
	for n > 0 {
		k := n
		if k > len(w.buf) {
			k = len(w.buf)
		}
		if err := w.write(w.buf[:k]); err != nil {
			return err
		}
		n -= k
	}
	return nil
}

// writeFileData copies every file's contents into its allocated extents,
// padding each file out to a sector boundary.
//
// Sources are opened here, during the writing pass, and each is checked
// against the size it reported during layout: a file that changed on disc
// between the two passes would otherwise silently shift every subsequent
// extent and corrupt the whole image.
func (l *layout) writeFileData(w *sectorWriter) error {
	for _, f := range l.files {
		var total uint64
		for _, s := range f.sections {
			total += uint64(s.length)
		}
		if total == 0 {
			continue
		}
		rc, err := f.src.Open()
		if err != nil {
			return fmt.Errorf("opening %q: %w", f.hostName, err)
		}
		n, err := io.Copy(w, io.LimitReader(rc, int64(total)+1))
		rc.Close()
		if err != nil {
			return fmt.Errorf("copying %q: %w", f.hostName, err)
		}
		if uint64(n) != total {
			return fmt.Errorf("iso: %q changed size between layout and write: laid out %d bytes, read %d",
				f.hostName, total, n)
		}
		if err := w.padToSector(); err != nil {
			return err
		}
	}
	return nil
}

// Write makes sectorWriter an io.Writer so that io.Copy can stream file
// contents through it while keeping the byte count accurate.
func (w *sectorWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.n += int64(n)
	return n, err
}

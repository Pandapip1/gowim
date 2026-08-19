package iso

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// Source supplies the contents of one file to be recorded in the image.
//
// It exists so that the image can contain files that are not on disk. The
// El Torito phase in particular has to record a boot catalog that is
// generated during layout and never exists as a host file, and the WIM
// side of gowim may eventually want to stream a rebuilt image straight into
// an ISO without staging it. Size is consulted during layout, before any
// bytes are written, because extents must be allocated before the volume
// descriptors that reference them can be produced.
//
// Size must return the same value it returned during layout when the data
// is later read, and Open must yield exactly that many bytes. A mismatch is
// detected and reported as an error rather than silently producing a
// corrupt image.
type Source interface {
	// Size returns the length of the file in bytes.
	Size() (int64, error)
	// Open returns a reader over the file's contents.
	Open() (io.ReadCloser, error)
}

// FileSource is a Source backed by a host filesystem path.
type FileSource string

// Size reports the size of the host file.
func (f FileSource) Size() (int64, error) {
	st, err := os.Stat(string(f))
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// Open opens the host file for reading.
func (f FileSource) Open() (io.ReadCloser, error) { return os.Open(string(f)) }

// MemSource is a Source backed by an in-memory byte slice. It is what the
// later El Torito phase will use for the generated boot catalog, and what
// the tests use to build small trees without touching disk.
type MemSource []byte

// Size reports the length of the slice.
func (m MemSource) Size() (int64, error) { return int64(len(m)), nil }

// Open returns a reader over the slice.
func (m MemSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(m)), nil
}

// section is one File Section of a file, i.e. one Extent and hence one
// Directory Record (ECMA-119 6.5.1, 6.4.1). All but the last section of a
// file carry the Multi-Extent flag of 9.1.6 bit 7.
type section struct {
	extent uint32 // Logical Block Number of the first block (9.1.3)
	length uint32 // Data Length in bytes (9.1.4)
}

// node is one entry in the logical tree being written.
type node struct {
	// hostName is the name as given by the caller, before mangling. It is
	// retained for error messages and for the later Joliet phase, which
	// needs the original name rather than the ECMA-119 mangling.
	hostName string
	// id is the mangled ECMA-119 identifier: a Directory Identifier (7.6)
	// for a directory, or the File Name plus SEPARATOR 1 plus File Name
	// Extension (7.5.1) for a file, without the version number.
	id string

	isDir    bool
	src      Source
	modTime  time.Time
	children []*node
	parent   *node

	// Assigned during layout.
	sections  []section
	dirExtent uint32 // first Logical Block of the directory's own extent
	dirLength uint32 // byte length of the directory's extent
	pathIndex uint16 // ordinal in the Path Table (6.9), directories only
	level     int    // depth in the hierarchy, Root Directory being 1 (6.8.2)
}

// name and ext split the mangled file identifier for sorting per 9.3.
func (n *node) nameExt() (string, string) {
	if n.isDir {
		return n.id, ""
	}
	if i := strings.LastIndexByte(n.id, '.'); i >= 0 {
		return n.id[:i], n.id[i+1:]
	}
	return n.id, ""
}

// Options configures the image being written. The zero value is usable:
// it produces a Level1 image with an empty volume identifier and no
// timestamps.
type Options struct {
	// VolumeID becomes the Volume Identifier (ECMA-119 8.4.6), a
	// 32-character d-character field. It is what most operating systems
	// display as the disc label. Characters that are not d-characters are
	// folded to '_' and the value is truncated to 32 bytes.
	VolumeID string

	// SystemID becomes the System Identifier (8.4.5): "an identification
	// of a system which can recognize and act upon the content of the
	// Logical Sectors with Logical Sector Numbers 0 to 15", i.e. the
	// System Area. 32 a-characters.
	SystemID string

	// VolumeSetID, PublisherID, PreparerID and ApplicationID become the
	// correspondingly named identifier fields of 8.4.19 to 8.4.22. Each is
	// a 128-byte field: d-characters for the Volume Set Identifier,
	// a-characters for the other three.
	VolumeSetID   string
	PublisherID   string
	PreparerID    string
	ApplicationID string

	// Timestamp is recorded as the Volume Creation, Modification and
	// Effective date-and-times (8.4.26, 8.4.27, 8.4.29) and as the
	// Recording Date and Time of every Directory Record (9.1.5) for
	// entries that do not carry their own modification time. The zero
	// value records the "not specified" form both clauses define.
	Timestamp time.Time

	// Level selects the ECMA-119 clause 10 level of interchange. The zero
	// value is treated as Level1. Splitting a file across File Sections
	// requires Level3.
	Level InterchangeLevel

	// MaxSectionSize caps the Data Length of a single File Section, i.e.
	// how much of a file one Directory Record describes. Zero means the
	// default, maxSectionSizeDefault.
	//
	// This exists mainly so tests can exercise the multi-extent path
	// without materialising a 4 GiB file, but it is also the knob a
	// caller would use to match another producer's splitting behaviour.
	// It is rounded down to a multiple of LogicalSectorSize because every
	// File Section but the last must end on an Extent boundary: an Extent
	// is a set of Logical Blocks (6.4.1), so a section that stopped
	// mid-block would leave the next section unable to start at a block.
	MaxSectionSize uint32

	// PadSectors is the number of zero sectors appended after all data.
	// genisoimage appends 150 by default as a run-out for CD-R drives
	// whose read-ahead runs off the end of the recorded area. It is not
	// required by ECMA-119 and defaults to 0 here. Note that a later UDF
	// phase will impose its own constraint on the tail of the image,
	// because ECMA-167 pins an Anchor Volume Descriptor Pointer near the
	// last recorded sector.
	PadSectors uint32
}

// maxSectionSizeDefault is the largest Data Length this package will put in
// a single Directory Record.
//
// The hard ceiling is imposed by ECMA-119 9.1.4: the Data Length field is a
// 32-bit number (recorded twice, per 7.3.3), so one File Section can be at
// most 4 GiB - 1 bytes. The value used here is that ceiling rounded down to
// a whole number of Logical Sectors, 0xFFFFF800 = 4294965248 bytes, because
// a non-final File Section must end on an Extent boundary.
//
// This is the same threshold genisoimage tests against when it decides a
// file is too big: cdrkit-1.1.11/genisoimage/tree.c line 1554 warns when
// st_size >= 0xFFFFFFFF.
const maxSectionSizeDefault = 0xFFFFF800

// Builder accumulates a logical tree and then serialises it as an image.
//
// A Builder is single-use: call the Add methods, then WriteTo once.
type Builder struct {
	opts Options
	root *node
	err  error
}

// New returns a Builder for an image described by opts. A nil opts is
// equivalent to a zero Options.
func New(opts *Options) *Builder {
	b := &Builder{}
	if opts != nil {
		b.opts = *opts
	}
	if b.opts.Level == 0 {
		b.opts.Level = Level1
	}
	if b.opts.MaxSectionSize == 0 {
		b.opts.MaxSectionSize = maxSectionSizeDefault
	}
	b.opts.MaxSectionSize -= b.opts.MaxSectionSize % LogicalSectorSize
	b.root = &node{hostName: "", isDir: true, modTime: b.opts.Timestamp, level: 1}
	return b
}

// AddDir creates a directory at isoPath, and any missing parents.
//
// isoPath is a slash-separated path relative to the root of the image;
// leading and trailing slashes are ignored. The path components are the
// caller's *host* names and are mangled into Directory Identifiers when the
// image is written, so callers should not pre-mangle them.
func (b *Builder) AddDir(isoPath string) error {
	if b.err != nil {
		return b.err
	}
	_, err := b.mkdirAll(splitPath(isoPath))
	return err
}

// AddFile records src at isoPath, creating any missing parent directories.
func (b *Builder) AddFile(isoPath string, src Source) error {
	if b.err != nil {
		return b.err
	}
	parts := splitPath(isoPath)
	if len(parts) == 0 {
		return errors.New("iso: AddFile: empty path")
	}
	dir, err := b.mkdirAll(parts[:len(parts)-1])
	if err != nil {
		return err
	}
	name := parts[len(parts)-1]
	if dir.find(name) != nil {
		return fmt.Errorf("iso: AddFile: %q already exists", isoPath)
	}
	dir.children = append(dir.children, &node{
		hostName: name,
		src:      src,
		modTime:  b.opts.Timestamp,
		parent:   dir,
		level:    dir.level + 1,
	})
	return nil
}

// AddTree recursively adds the *contents* of the host directory hostDir at
// isoPath in the image (isoPath may be "" or "/" for the image root).
//
// Only regular files and directories are recorded. Symbolic links are
// followed, and anything that is still neither a regular file nor a
// directory after that (devices, sockets, FIFOs) is skipped: plain
// ISO 9660 with no Rock Ridge extension has no way to represent them, and
// silently recording a device node as an empty file would be worse than
// omitting it. Modification times are taken from the host files, so the
// Recording Date and Time of each Directory Record (ECMA-119 9.1.5)
// reflects the source rather than the build time.
func (b *Builder) AddTree(isoPath, hostDir string) error {
	if b.err != nil {
		return b.err
	}
	dir, err := b.mkdirAll(splitPath(isoPath))
	if err != nil {
		return err
	}
	return b.addTreeInto(dir, hostDir)
}

func (b *Builder) addTreeInto(dir *node, hostDir string) error {
	entries, err := os.ReadDir(hostDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		hostPath := path.Join(hostDir, e.Name())
		// Stat, not Lstat: symlinks are followed (see AddTree).
		st, err := os.Stat(hostPath)
		if err != nil {
			return err
		}
		switch {
		case st.IsDir():
			child := &node{
				hostName: e.Name(),
				isDir:    true,
				modTime:  st.ModTime(),
				parent:   dir,
				level:    dir.level + 1,
			}
			dir.children = append(dir.children, child)
			if err := b.addTreeInto(child, hostPath); err != nil {
				return err
			}
		case st.Mode().IsRegular():
			dir.children = append(dir.children, &node{
				hostName: e.Name(),
				src:      FileSource(hostPath),
				modTime:  st.ModTime(),
				parent:   dir,
				level:    dir.level + 1,
			})
		}
	}
	return nil
}

func (b *Builder) mkdirAll(parts []string) (*node, error) {
	cur := b.root
	for _, p := range parts {
		next := cur.find(p)
		if next == nil {
			next = &node{
				hostName: p,
				isDir:    true,
				modTime:  b.opts.Timestamp,
				parent:   cur,
				level:    cur.level + 1,
			}
			cur.children = append(cur.children, next)
		} else if !next.isDir {
			return nil, fmt.Errorf("iso: %q exists as a file", p)
		}
		cur = next
	}
	return cur, nil
}

func (n *node) find(hostName string) *node {
	for _, c := range n.children {
		if c.hostName == hostName {
			return c
		}
	}
	return nil
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// finalize mangles every identifier, resolves collisions, and sorts each
// directory's children into the order ECMA-119 9.3 requires. It must run
// before layout, since the sizes of directory extents depend on the final
// identifier lengths.
func (b *Builder) finalize() error {
	if err := b.checkDepth(b.root); err != nil {
		return err
	}
	b.mangle(b.root)
	return nil
}

// checkDepth enforces ECMA-119 6.8.2.1: "For a Directory Hierarchy
// identified in a Primary Volume Descriptor ... the number of levels in the
// hierarchy shall not exceed eight."
//
// This is a real limit that real Windows media exceeds, which is why
// genisoimage's -iso-level 4 sets RR_relocation_depth to 32767 and why real
// producers rely on the Enhanced Volume Descriptor (for which 6.8.2.1
// explicitly permits more than eight levels) or on Rock Ridge deep-
// directory relocation. This package implements neither yet, so it reports
// the violation rather than emitting a non-conformant image and letting the
// caller discover the problem on a reader that enforces it.
func (b *Builder) checkDepth(n *node) error {
	if n.level > 8 {
		return fmt.Errorf("iso: directory hierarchy deeper than the 8 levels ECMA-119 6.8.2.1 allows "+
			"for a Primary Volume Descriptor (at %q, level %d); this needs the Enhanced Volume "+
			"Descriptor or Rock Ridge relocation, neither of which is implemented yet", n.hostName, n.level)
	}
	for _, c := range n.children {
		if c.isDir {
			if err := b.checkDepth(c); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *Builder) mangle(dir *node) {
	// Mangle in a deterministic order so that collision resolution is
	// reproducible: two runs over the same tree must produce the same
	// image. os.ReadDir already sorts by name, but AddFile callers may
	// not, so sort by host name before mangling.
	sort.SliceStable(dir.children, func(i, j int) bool {
		return dir.children[i].hostName < dir.children[j].hostName
	})

	// Directory Identifiers and File Identifiers share one namespace
	// within a directory, since both appear as the File Identifier field
	// of a Directory Record (9.1.11) and 9.3 orders them together.
	used := map[string]bool{}
	for _, c := range dir.children {
		if c.isDir {
			c.id = dedupe(used, mangleDirName(c.hostName, b.opts.Level), true, b.opts.Level)
		} else {
			c.id = dedupe(used, mangleFileName(c.hostName, b.opts.Level), false, b.opts.Level)
		}
	}

	sort.SliceStable(dir.children, func(i, j int) bool {
		an, ae := dir.children[i].nameExt()
		bn, be := dir.children[j].nameExt()
		// Every file this package writes has File Version Number 1
		// (7.5.1 requires a number from 1 to 32767), so the version
		// criterion of 9.3 never distinguishes two entries here.
		return compareFileIdentifiers(an, ae, 1, bn, be, 1) < 0
	})

	for _, c := range dir.children {
		if c.isDir {
			b.mangle(c)
		}
	}
}

// dirs returns every directory in the tree in the order ECMA-119 6.9.1
// requires of Path Table Records: ascending by level in the hierarchy,
// then ascending by the directory number of the parent, then ascending by
// Directory Identifier. The Root Directory is first and gets record
// number 1 (6.9).
//
// That ordering is a plain breadth-first traversal *provided* each level is
// visited in parent-then-identifier order, which holds because the previous
// level was itself emitted in that order and each directory's children were
// already sorted by identifier in mangle. The Path Table index is assigned
// here so that Directory Records and Path Table Records agree.
func (b *Builder) dirs() []*node {
	out := []*node{b.root}
	b.root.pathIndex = 1
	for i := 0; i < len(out); i++ {
		for _, c := range out[i].children {
			if c.isDir {
				c.pathIndex = uint16(len(out) + 1)
				out = append(out, c)
			}
		}
	}
	return out
}

// files returns every non-directory node, in the order their extents will
// be allocated: a depth-first walk in directory order. Keeping file data in
// tree order is not required by ECMA-119, but it matches what genisoimage
// does and it keeps the data for one directory contiguous, which matters
// for seek behaviour on optical media.
func (b *Builder) files() []*node {
	var out []*node
	var walk func(*node)
	walk = func(n *node) {
		for _, c := range n.children {
			if c.isDir {
				walk(c)
			} else {
				out = append(out, c)
			}
		}
	}
	walk(b.root)
	return out
}

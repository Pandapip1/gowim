package wim

import (
	"crypto/rand"
	"crypto/sha1"
	"fmt"
	"io"
)

// BlobSource supplies a blob's raw, uncompressed content bytes given its
// hash. WriteTo/Assemble call it once per entry in the caller's BlobTable, so
// implementations may stream content from disk (or wherever it lives) rather
// than requiring every blob to be resident in memory at once -- useful for
// WIMs whose total uncompressed content is much larger than is comfortable
// to hold in a single []byte (e.g. a Windows install image's several
// gigabytes of payload).
type BlobSource interface {
	Blob(h Hash) ([]byte, error)
}

// BlobSourceFunc adapts a function to a BlobSource.
type BlobSourceFunc func(h Hash) ([]byte, error)

// Blob calls f.
func (f BlobSourceFunc) Blob(h Hash) ([]byte, error) { return f(h) }

// MapBlobSource adapts a map of hash to content bytes to a BlobSource, for
// callers who already hold every blob's bytes in memory at once. Looking up a
// hash not present in the map is an error.
type MapBlobSource map[Hash][]byte

// Blob returns m[h], or an error if h is not present.
func (m MapBlobSource) Blob(h Hash) ([]byte, error) {
	data, ok := m[h]
	if !ok {
		return nil, fmt.Errorf("wim: MapBlobSource: no data for hash %s", h)
	}
	return data, nil
}

// WriteOptions configures WriteTo/Assemble.
type WriteOptions struct {
	// CompressionType is CompressionNone (store every resource uncompressed)
	// or one of the HdrFlagCompress{XPRESS,XPRESS2,LZX,LZMS} constants. A WIM
	// has exactly one compression type for its whole container (there is no
	// per-blob choice); this is passed straight through to EncodeResourceData
	// for every blob and metadata resource.
	CompressionType CompressionType
	// ChunkSize is the uncompressed chunk size used to frame every compressed
	// resource (Header.ChunkSize). It must be nonzero if CompressionType is
	// not CompressionNone; it is ignored (and the written header stores 0)
	// when CompressionType is CompressionNone.
	ChunkSize uint32
	// BootIndex is the 1-based index into the images slice passed to
	// WriteTo/Assemble designating the bootable image, or 0 if no image is
	// bootable (matching Header.BootIndex's own convention).
	BootIndex int
	// GUID is stored in the header. If it is the zero GUID, a random one is
	// generated.
	GUID GUID
	// ComputeIntegrityTable, if true, makes WriteTo compute and append an
	// integrity table (SHA-1 digests of fixed-size chunks of the file) and
	// set Header.IntegrityTable accordingly, mirroring DISM/wimlib's
	// /CheckIntegrity. See IntegrityChunkSize below and the doc comment on
	// integrityAccumulator (integrity_write.go) for the exact, empirically
	// confirmed byte range this covers.
	ComputeIntegrityTable bool
	// IntegrityChunkSize is the chunk size used when ComputeIntegrityTable is
	// set. Zero uses the package constant IntegrityChunkSize (10 MiB),
	// matching wimlib's default. Ignored when ComputeIntegrityTable is false.
	IntegrityChunkSize uint32
}

// WriteTo assembles a complete, valid, standalone (single-part, non-solid, no
// integrity table) WIM file and writes it to w, which must support both
// writing and seeking: the header is written first as a zero-filled
// placeholder (its final contents depend on every other resource's eventual
// offset, which isn't known until they've all been written), and w.Seek is
// used to go back and patch it in once assembly is complete.
//
// images supplies one *ImageMetadata per image, in the order they should be
// numbered (image 1 is images[0], and so on); WriteTo writes one metadata
// resource per image and sets Header.ImageCount accordingly. Building each
// image's directory-entry tree/security data is the caller's job (see
// DirEntry.Add and the sibling driver package's Install).
//
// bt is the caller's already-built blob table: every entry's Hash, RefCount,
// and PartNumber must already be correct (WriteTo does not walk any dentry
// tree to compute reference counts), but Resource is overwritten by WriteTo
// with the real on-disk resource header once the blob's payload has been
// placed. blobs supplies each entry's raw, uncompressed content by hash.
// WriteTo also appends one further BlobDescriptor per image (flagged
// ResFlagMetadata) for that image's own metadata resource, so bt ends up
// exactly as BlobTable.MetadataResources() expects to find it. Because bt is
// mutated in place, a given *BlobTable must not be passed to WriteTo/Assemble
// more than once (a second call would duplicate the metadata entries).
//
// xmlData supplies the WIM's XML data (one <IMAGE INDEX="n"> element per
// image, in index order); WriteTo only serializes Document, it does not
// generate or validate the per-image XML content.
//
// Confirmed against a real Windows 11 23H2 install image's boot.wim and
// install.esd (2026-07-10, via this package's own Reader plus
// `wimlib-imagex info`): in a real compressed WIM, the blob table and XML
// data resources are stored uncompressed (ResourceHeader.Flags ==
// ResFlagMetadata only, ResFlagCompressed never set) while each image's
// metadata resource is compressed the same way file-data blobs are
// (ResFlagMetadata|ResFlagCompressed, non-solid even in the LZMS-compressed
// install.esd). WriteTo matches this: blob table and XML data are written raw
// via AppendTo, never through EncodeResourceData; metadata resources are
// compressed via EncodeResourceData like any other blob, with ResFlagMetadata
// ORed onto the returned flags.
//
// Version selection also follows real-world convention confirmed against the
// same two files: boot.wim (LZX) has Version 0x10d00 (VersionDefault) and
// install.esd (LZMS) has Version 0xe00 (VersionSolid). WriteTo picks
// VersionSolid when opts.CompressionType is HdrFlagCompressLZMS and
// VersionDefault otherwise (including for CompressionNone and both XPRESS
// variants).
//
// Solid resources are out of scope: WriteTo never emits ResFlagSolid.
// Multi-part (split) WIMs are also out of scope: Header.PartNumber/TotalParts
// are always written as 1/1.
//
// An integrity table is computed and appended when opts.ComputeIntegrityTable
// is set (Header.IntegrityTable is left zero/absent otherwise, as before).
// This is done in the same single pass as everything else, not as a separate
// post-process re-read of the file: WriteTo already has every relevant byte
// (each blob's encoded payload, each image's encoded metadata payload, and
// the blob table's serialized bytes) in hand as it writes them in file order,
// so it feeds them to an integrityAccumulator as it goes and finalizes the
// table right after the blob table is written -- before writing XML data --
// matching the real, confirmed convention that the integrity table covers
// [HeaderSize, end of blob table) and excludes the XML data and the
// integrity table itself, even though the table is physically appended to
// the file last (after XML data). See integrity_write.go's doc comment for
// the empirical evidence (against a real boot.wim/install.esd) and wimlib
// source corroboration.
func WriteTo(w io.WriteSeeker, images []*ImageMetadata, bt *BlobTable, xmlData *XMLData, blobs BlobSource, opts WriteOptions) (int64, error) {
	if len(images) == 0 {
		return 0, fmt.Errorf("wim: WriteTo: no images")
	}
	if len(images) > MaxImages {
		return 0, fmt.Errorf("wim: WriteTo: %d images exceeds MaxImages (%d)", len(images), MaxImages)
	}
	if bt == nil {
		return 0, fmt.Errorf("wim: WriteTo: nil blob table")
	}
	if xmlData == nil {
		return 0, fmt.Errorf("wim: WriteTo: nil XML data")
	}
	if blobs == nil {
		return 0, fmt.Errorf("wim: WriteTo: nil blob source")
	}
	if opts.BootIndex < 0 || opts.BootIndex > len(images) {
		return 0, fmt.Errorf("wim: WriteTo: boot index %d out of range for %d image(s)", opts.BootIndex, len(images))
	}
	if opts.CompressionType != CompressionNone && opts.ChunkSize == 0 {
		return 0, fmt.Errorf("wim: WriteTo: chunk size must be nonzero for compression type %#x", opts.CompressionType)
	}

	// Reserve the header's fixed-size slot at the very start of the file;
	// its real contents are patched in at the end, once every other
	// resource's offset is known.
	if _, err := w.Write(make([]byte, HeaderSize)); err != nil {
		return 0, wrapErr("write header placeholder", err)
	}
	offset := uint64(HeaderSize)

	writeBytes := func(b []byte) error {
		if len(b) == 0 {
			return nil
		}
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		offset += uint64(n)
		return nil
	}

	chunkSize := opts.ChunkSize
	if opts.CompressionType == CompressionNone {
		chunkSize = 0
	}

	var integ *integrityAccumulator
	if opts.ComputeIntegrityTable {
		integ = newIntegrityAccumulator(opts.IntegrityChunkSize)
	}

	// Blob content, one resource per already-deduplicated blob-table entry.
	for i := range bt.Entries {
		hash := bt.Entries[i].Hash
		data, err := blobs.Blob(hash)
		if err != nil {
			return 0, wrapErr(fmt.Sprintf("blob %s", hash), err)
		}
		payload, flags, err := EncodeResourceData(data, opts.CompressionType, chunkSize)
		if err != nil {
			return 0, wrapErr(fmt.Sprintf("encode blob %s", hash), err)
		}
		res := ResourceHeader{
			SizeInWIM:        uint64(len(payload)),
			Flags:            flags,
			OffsetInWIM:      offset,
			UncompressedSize: uint64(len(data)),
		}
		if integ != nil {
			integ.write(payload)
		}
		if err := writeBytes(payload); err != nil {
			return 0, wrapErr(fmt.Sprintf("write blob %s", hash), err)
		}
		bt.Entries[i].Resource = res
	}

	// One metadata resource per image.
	var bootMetaRes ResourceHeader
	for i, im := range images {
		if im == nil {
			return 0, fmt.Errorf("wim: WriteTo: image %d is nil", i+1)
		}
		metaBytes, err := im.AppendTo(nil)
		if err != nil {
			return 0, wrapErr(fmt.Sprintf("image %d metadata", i+1), err)
		}
		payload, flags, err := EncodeResourceData(metaBytes, opts.CompressionType, chunkSize)
		if err != nil {
			return 0, wrapErr(fmt.Sprintf("encode image %d metadata", i+1), err)
		}
		res := ResourceHeader{
			SizeInWIM:        uint64(len(payload)),
			Flags:            flags | ResFlagMetadata,
			OffsetInWIM:      offset,
			UncompressedSize: uint64(len(metaBytes)),
		}
		if integ != nil {
			integ.write(payload)
		}
		if err := writeBytes(payload); err != nil {
			return 0, wrapErr(fmt.Sprintf("write image %d metadata", i+1), err)
		}
		metaHash := Hash(sha1.Sum(metaBytes))
		bt.Entries = append(bt.Entries, BlobDescriptor{
			Resource:   res,
			PartNumber: 1,
			RefCount:   1,
			Hash:       metaHash,
		})
		if opts.BootIndex == i+1 {
			bootMetaRes = res
		}
	}

	// Blob table and XML data: both stored uncompressed and flagged as
	// metadata-class resources, matching real WIMs (see doc comment above).
	btBytes, err := bt.AppendTo(nil)
	if err != nil {
		return 0, wrapErr("blob table", err)
	}
	btRes := ResourceHeader{
		SizeInWIM:        uint64(len(btBytes)),
		Flags:            ResFlagMetadata,
		OffsetInWIM:      offset,
		UncompressedSize: uint64(len(btBytes)),
	}
	if integ != nil {
		integ.write(btBytes)
	}
	if err := writeBytes(btBytes); err != nil {
		return 0, wrapErr("write blob table", err)
	}

	// The integrity table's coverage ends here, at the end of the blob table
	// -- it does not cover the XML data below (see integrity_write.go's doc
	// comment for the confirmed convention and evidence). It is written to
	// disk further below, after XML data, matching real WIMs' physical
	// layout, but its *content* is finalized now.
	var integHashes []Hash
	integChunkSize := opts.IntegrityChunkSize
	if integChunkSize == 0 {
		integChunkSize = IntegrityChunkSize
	}
	if integ != nil {
		integHashes = integ.finish()
	}

	xmlBytes := xmlData.AppendTo(nil)
	xmlRes := ResourceHeader{
		SizeInWIM:        uint64(len(xmlBytes)),
		Flags:            ResFlagMetadata,
		OffsetInWIM:      offset,
		UncompressedSize: uint64(len(xmlBytes)),
	}
	if err := writeBytes(xmlBytes); err != nil {
		return 0, wrapErr("write xml data", err)
	}

	// Integrity table, written last (after XML data), matching real WIMs'
	// physical layout, even though its coverage range excludes the XML data
	// it's written after.
	var integRes ResourceHeader
	if integ != nil {
		it := &IntegrityTable{ChunkSize: integChunkSize, Hashes: integHashes}
		itBytes := it.AppendTo(nil)
		integRes = ResourceHeader{
			SizeInWIM:        uint64(len(itBytes)),
			OffsetInWIM:      offset,
			UncompressedSize: uint64(len(itBytes)),
		}
		if err := writeBytes(itBytes); err != nil {
			return 0, wrapErr("write integrity table", err)
		}
	}

	guid := opts.GUID
	if guid == (GUID{}) {
		if _, err := rand.Read(guid[:]); err != nil {
			return 0, wrapErr("generate GUID", err)
		}
	}

	version := VersionDefault
	if opts.CompressionType == HdrFlagCompressLZMS {
		version = VersionSolid
	}

	var flags uint32
	if opts.CompressionType != CompressionNone {
		flags = HdrFlagCompression | opts.CompressionType
	}

	hdr := Header{
		Magic:          Magic,
		Version:        version,
		Flags:          flags,
		ChunkSize:      chunkSize,
		GUID:           guid,
		PartNumber:     1,
		TotalParts:     1,
		ImageCount:     uint32(len(images)),
		BlobTable:      btRes,
		XMLData:        xmlRes,
		BootMetadata:   bootMetaRes,
		BootIndex:      uint32(opts.BootIndex),
		IntegrityTable: integRes,
	}
	hdrBytes, err := hdr.AppendTo(nil)
	if err != nil {
		return 0, wrapErr("header", err)
	}
	if _, err := w.Seek(0, io.SeekStart); err != nil {
		return 0, wrapErr("seek to header", err)
	}
	if _, err := w.Write(hdrBytes); err != nil {
		return 0, wrapErr("patch header", err)
	}
	if _, err := w.Seek(int64(offset), io.SeekStart); err != nil {
		return 0, wrapErr("seek to end", err)
	}
	return int64(offset), nil
}

// sliceWriteSeeker is a minimal io.WriteSeeker backed by an in-memory byte
// slice, used by Assemble to reuse WriteTo's seek-back-and-patch-the-header
// logic without requiring callers to supply a real file.
type sliceWriteSeeker struct {
	buf []byte
	pos int64
}

func (s *sliceWriteSeeker) Write(p []byte) (int, error) {
	end := s.pos + int64(len(p))
	if end > int64(len(s.buf)) {
		grown := make([]byte, end)
		copy(grown, s.buf)
		s.buf = grown
	}
	copy(s.buf[s.pos:end], p)
	s.pos = end
	return len(p), nil
}

func (s *sliceWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = s.pos
	case io.SeekEnd:
		base = int64(len(s.buf))
	default:
		return 0, fmt.Errorf("wim: sliceWriteSeeker: invalid whence %d", whence)
	}
	pos := base + offset
	if pos < 0 {
		return 0, fmt.Errorf("wim: sliceWriteSeeker: negative seek position")
	}
	s.pos = pos
	return pos, nil
}

// Assemble is the in-memory convenience form of WriteTo: it lays out and
// serializes a complete WIM file the same way, returning the result as a
// single byte slice rather than requiring an io.WriteSeeker. Prefer WriteTo
// (writing directly to an *os.File) for large WIMs, since Assemble holds the
// entire output in memory at once.
func Assemble(images []*ImageMetadata, bt *BlobTable, xmlData *XMLData, blobs BlobSource, opts WriteOptions) ([]byte, error) {
	var sws sliceWriteSeeker
	n, err := WriteTo(&sws, images, bt, xmlData, blobs, opts)
	if err != nil {
		return nil, err
	}
	return sws.buf[:n], nil
}

package cab

import (
	"encoding/binary"
	"fmt"
)

// decodeFolder decompresses every CFDATA block belonging to f, in order,
// and returns the folder's full decompressed byte stream. Results are
// memoized on the Reader since multiple Files can share one Folder.
func (r *Reader) decodeFolder(f *Folder) ([]byte, error) {
	if r.decoded == nil {
		r.decoded = make(map[*Folder][]byte)
	}
	if b, ok := r.decoded[f]; ok {
		return b, nil
	}

	// Concatenate every CFDATA block's compressed payload for this folder,
	// and remember each block's declared uncompressed size -- for LZX
	// these declared sizes drive the frame-by-frame output exactly as
	// libmspack's lzxd_decompress does (see cablzx.go), and for "stored"
	// data they're just how much to copy straight through.
	off := int(f.dataStart)
	var compressed []byte
	var uncompSizes []int
	for i := 0; i < int(f.numDataBlocks); i++ {
		if off+8 > len(r.data) {
			return nil, ErrTruncated
		}
		csum := binary.LittleEndian.Uint32(r.data[off : off+4])
		cbData := int(binary.LittleEndian.Uint16(r.data[off+4 : off+6]))
		cbUncomp := int(binary.LittleEndian.Uint16(r.data[off+6 : off+8]))
		off += 8 + r.cbCFData
		if off+cbData > len(r.data) {
			return nil, ErrTruncated
		}
		block := r.data[off : off+cbData]
		off += cbData

		if csum != 0 {
			if got := cabChecksum(block, uint16(cbData), uint16(cbUncomp)); got != csum {
				return nil, fmt.Errorf("%w: folder data block %d: got %#x, want %#x", ErrBadChecksum, i, got, csum)
			}
		}

		compressed = append(compressed, block...)
		uncompSizes = append(uncompSizes, cbUncomp)
	}

	var out []byte
	var err error
	switch f.Method {
	case CompressNone:
		out = append([]byte(nil), compressed...)
	case CompressLZX:
		out, err = lzxCabDecompress(compressed, uncompSizes, f.WindowBits)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCompression, f.Method)
	}
	if err != nil {
		return nil, err
	}

	r.decoded[f] = out
	return out, nil
}

// cabChecksum implements [MS-CAB] 2.6's CFDATA checksum algorithm
// (a variant of the "cksum"-style rolling checksum documented directly in
// [MS-CAB], reproduced identically -- not reverse-engineered -- from
// libmspack's mspack/cabd.c cab_checksum/cab_checksum_partial, since
// [MS-CAB] itself gives only prose, no pseudocode). It folds cbData and
// cbUncomp's own bytes in at the end, matching the data actually seen on
// disk, and only used here to validate blocks (a zero csum field means "not
// computed", per [MS-CAB], and is skipped above).
func cabChecksum(data []byte, cbData, cbUncomp uint16) uint32 {
	var csum uint32
	n := len(data)
	i := 0
	for ; i+4 <= n; i += 4 {
		csum ^= uint32(data[i]) | uint32(data[i+1])<<8 | uint32(data[i+2])<<16 | uint32(data[i+3])<<24
	}
	// The tail (0-3 leftover bytes) is folded in libmspack's own byte
	// order, derived from mspack/cabd.c's cabd_checksum: successive
	// fallthrough cases each do "*data++", so a 3-byte tail packs as
	// (data[0]<<16)|(data[1]<<8)|data[2], not the little-endian order a
	// naive port might assume -- confirmed against this package's real
	// test cabinet, where blocks with a non-multiple-of-4 remainder
	// (verified against every real CFDATA block in
	// testdata/OpenSSH-Server-Package-amd64.cab) only check out with this
	// exact byte order.
	var ul uint32
	switch n - i {
	case 3:
		ul = uint32(data[i])<<16 | uint32(data[i+1])<<8 | uint32(data[i+2])
	case 2:
		ul = uint32(data[i])<<8 | uint32(data[i+1])
	case 1:
		ul = uint32(data[i])
	}
	csum ^= ul
	trailer := uint32(cbData) | uint32(cbUncomp)<<16
	csum ^= trailer
	return csum
}

// Extract returns the decompressed bytes of the named file.
func (r *Reader) Extract(name string) ([]byte, error) {
	for _, f := range r.Files {
		if f.Name == name {
			return r.ExtractFile(f)
		}
	}
	return nil, ErrFileNotFound
}

// ExtractFile returns f's decompressed bytes, decompressing (and caching)
// its whole folder if this is the first file read from it.
func (r *Reader) ExtractFile(f *File) ([]byte, error) {
	folderData, err := r.decodeFolder(f.folder)
	if err != nil {
		return nil, err
	}
	start := int(f.FolderOffset)
	end := start + int(f.Size)
	if start < 0 || end > len(folderData) || start > end {
		return nil, fmt.Errorf("cab: file %q offset/size out of range of decompressed folder data (folder is %d bytes)", f.Name, len(folderData))
	}
	return folderData[start:end], nil
}

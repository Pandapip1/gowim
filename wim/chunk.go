package wim

import "fmt"

// CompressionType identifies which codec a WIM's compressed resources use. It
// takes one of the values below (mirroring the HdrFlagCompress* constants in
// header.go, plus CompressionNone for "no compression").
type CompressionType = uint32

// CompressionNone indicates that a resource should be (or is) stored
// uncompressed: no chunk table, no ResFlagCompressed bit, raw bytes on disk.
const CompressionNone CompressionType = 0

// CompressionType resolves the WIM's compression codec from the header's
// Flags, mirroring wimlib's src/wim.c logic in its WIM-opening path (checked
// against wimlib commit cd5e231, 2026-01-29):
//
//	if (wim->hdr.flags & WIM_HDR_FLAG_COMPRESSION) {
//		if (wim->hdr.flags & WIM_HDR_FLAG_COMPRESS_LZX) {
//			wim->compression_type = WIMLIB_COMPRESSION_TYPE_LZX;
//		} else if (wim->hdr.flags & (WIM_HDR_FLAG_COMPRESS_XPRESS |
//					     WIM_HDR_FLAG_COMPRESS_XPRESS_2)) {
//			wim->compression_type = WIMLIB_COMPRESSION_TYPE_XPRESS;
//		} else if (wim->hdr.flags & WIM_HDR_FLAG_COMPRESS_LZMS) {
//			wim->compression_type = WIMLIB_COMPRESSION_TYPE_LZMS;
//		} else {
//			return WIMLIB_ERR_INVALID_COMPRESSION_TYPE;
//		}
//	} else {
//		wim->compression_type = WIMLIB_COMPRESSION_TYPE_NONE;
//	}
//
// Notably, HdrFlagCompressXPRESS2 is folded into plain XPRESS: wimlib checks
// it with a plain OR against HdrFlagCompressXPRESS and otherwise never
// distinguishes it anywhere in the codebase. Its own header comment
// (include/wimlib/header.h) admits uncertainty about its exact purpose --
// "XPRESS, with small chunk size???" -- so this is wimlib's own real,
// shipped behavior, not a guess made for this package: XPRESS2 resources are
// decoded and encoded identically to XPRESS ones. This function returns
// HdrFlagCompressXPRESS for both.
func (h Header) CompressionType() (CompressionType, error) {
	if h.Flags&HdrFlagCompression == 0 {
		return CompressionNone, nil
	}
	switch {
	case h.Flags&HdrFlagCompressLZX != 0:
		return HdrFlagCompressLZX, nil
	case h.Flags&(HdrFlagCompressXPRESS|HdrFlagCompressXPRESS2) != 0:
		return HdrFlagCompressXPRESS, nil
	case h.Flags&HdrFlagCompressLZMS != 0:
		return HdrFlagCompressLZMS, nil
	default:
		return 0, fmt.Errorf("wim: header has HdrFlagCompression set but no recognized compression-type flag (flags=%#x)", h.Flags)
	}
}

// chunkTableEntrySize returns the width in bytes of one chunk-table entry for
// a resource of the given uncompressed size: 4 bytes normally, 8 bytes if the
// resource is 4 GiB or larger. Mirrors wimlib's src/resource.c comment on the
// "original resource format" chunk table.
func chunkTableEntrySize(uncompressedSize uint64) int {
	if uncompressedSize > 0xFFFFFFFF {
		return 8
	}
	return 4
}

// numChunksFor returns ceil(uncompressedSize / chunkSize), the number of
// chunks a resource of the given uncompressed size is split into.
func numChunksFor(uncompressedSize uint64, chunkSize uint32) uint64 {
	cs := uint64(chunkSize)
	return (uncompressedSize + cs - 1) / cs
}

// chunkUncompressedSize returns the uncompressed size of chunk index i (0
// based) out of numChunks total chunks covering a resource of uncompressedSize
// bytes with the given chunk size: chunkSize for every chunk except the last,
// which gets the remainder (or chunkSize itself if the size divides evenly).
func chunkUncompressedSize(i, numChunks uint64, uncompressedSize uint64, chunkSize uint32) uint64 {
	if i != numChunks-1 {
		return uint64(chunkSize)
	}
	rem := uncompressedSize % uint64(chunkSize)
	if rem == 0 {
		return uint64(chunkSize)
	}
	return rem
}

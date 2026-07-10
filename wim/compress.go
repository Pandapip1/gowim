package wim

import (
	"fmt"

	"github.com/Pandapip1/gowim/lzms"
	"github.com/Pandapip1/gowim/lzx"
	"github.com/Pandapip1/gowim/xpress"
)

// compressChunk compresses one chunk's uncompressed bytes with the codec
// selected by ctype (one of the HdrFlagCompress* constants). It is the
// encode-side counterpart of decompressChunk.
func compressChunk(ctype CompressionType, data []byte) ([]byte, error) {
	switch ctype {
	case HdrFlagCompressXPRESS, HdrFlagCompressXPRESS2:
		return xpress.Compress(data), nil
	case HdrFlagCompressLZX:
		return lzx.Compress(data), nil
	case HdrFlagCompressLZMS:
		return lzms.Compress(data), nil
	default:
		return nil, fmt.Errorf("wim: compress: unrecognized compression type %#x", ctype)
	}
}

// EncodeResourceData takes a resource's full uncompressed bytes and produces
// the correctly-framed on-disk payload bytes plus the ResFlag* bits that
// should be set on that resource's ResourceHeader, for a non-solid resource.
//
// compressionType is one of the HdrFlagCompress{XPRESS,XPRESS2,LZX,LZMS}
// constants, or CompressionNone to force the resource to be stored
// uncompressed regardless of chunkSize. chunkSize is the WIM's chunk size
// (Header.ChunkSize); it is ignored when compressionType is CompressionNone.
//
// The returned flags is either 0 (store payload as-is, uncompressed, and set
// ResourceHeader.UncompressedSize == len(payload) == len(data)) or
// ResFlagCompressed (payload is chunk-table-framed compressed data; set
// ResourceHeader.UncompressedSize == len(data) and
// ResourceHeader.SizeInWIM == len(payload)).
//
// Two raw-storage fallbacks are implemented, both mirroring wimlib's actual
// writer behavior (src/write.c, src/compress_serial.c, cross-checked against
// wimlib commit cd5e231):
//
//   - Per chunk: each chunk is compressed and, if the compressed size is not
//     smaller than the chunk's uncompressed size, that chunk is stored raw
//     instead (see compress_serial.c's serial_chunk_compressor_signal_chunk_filled,
//     which asks the codec for at most usize-1 compressed bytes and falls
//     back to storing usize bytes raw on failure).
//   - Whole resource: after assembling the chunk-table-framed payload, if its
//     total size is not smaller than len(data), the resource is stored fully
//     raw instead (no chunk table, no compression flag) -- mirroring
//     wimlib's should_rewrite_blob_uncompressed/maybe_rewrite_blob_uncompressed,
//     which rewrites a resource whose "compressed" size_in_wim ended up >=
//     its uncompressed_size as a plain uncompressed resource. This also
//     naturally subsumes wimlib's documented single-chunk special case
//     (maybe_rewrite_blob_uncompressed: a resource with exactly one stored-raw
//     chunk and no chunk table is already byte-identical to the uncompressed
//     form, so it is reclassified rather than physically rewritten) --
//     assembling one chunk with no table yields exactly len(data) bytes,
//     which trips this same >= comparison.
func EncodeResourceData(data []byte, compressionType CompressionType, chunkSize uint32) (payload []byte, flags uint8, err error) {
	if len(data) == 0 {
		return nil, 0, nil
	}
	if compressionType == CompressionNone {
		return data, 0, nil
	}
	if chunkSize == 0 {
		return nil, 0, fmt.Errorf("wim: encode resource: chunk size must be nonzero for compression type %#x", compressionType)
	}

	uncompressedSize := uint64(len(data))
	numChunks := numChunksFor(uncompressedSize, chunkSize)

	chunks := make([][]byte, numChunks)
	for i := uint64(0); i < numChunks; i++ {
		start := i * uint64(chunkSize)
		usize := chunkUncompressedSize(i, numChunks, uncompressedSize, chunkSize)
		raw := data[start : start+usize]

		compressed, cerr := compressChunk(compressionType, raw)
		if cerr != nil {
			return nil, 0, fmt.Errorf("wim: encode resource: chunk %d: %w", i, cerr)
		}
		if uint64(len(compressed)) >= usize {
			// Compression did not shrink this chunk; store it raw.
			chunks[i] = raw
		} else {
			chunks[i] = compressed
		}
	}

	var out []byte
	if numChunks > 1 {
		entrySize := chunkTableEntrySize(uncompressedSize)
		table := make([]byte, int(numChunks-1)*entrySize)
		var off uint64
		for i := uint64(0); i < numChunks-1; i++ {
			off += uint64(len(chunks[i]))
			e := int(i) * entrySize
			if entrySize == 4 {
				le.PutUint32(table[e:e+4], uint32(off))
			} else {
				le.PutUint64(table[e:e+8], off)
			}
		}
		out = make([]byte, 0, len(table)+int(uncompressedSize))
		out = append(out, table...)
	} else {
		out = make([]byte, 0, len(chunks[0]))
	}
	for _, c := range chunks {
		out = append(out, c...)
	}

	if uint64(len(out)) >= uncompressedSize {
		// Whole-resource fallback: compression didn't help overall (or, for
		// the single-chunk case, the one chunk was stored raw and this is
		// already byte-identical to the plain uncompressed form).
		return data, 0, nil
	}
	return out, ResFlagCompressed, nil
}

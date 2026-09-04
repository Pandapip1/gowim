package wim

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/Pandapip1/gowim/lzms"
	"github.com/Pandapip1/gowim/lzx"
	"github.com/Pandapip1/gowim/xpress"
)

// compressChunk compresses one chunk's uncompressed bytes with the codec
// selected by ctype (one of the HdrFlagCompress* constants). It is the
// encode-side counterpart of decompressChunk.
//
// lzxOpts selects the LZX encoder's speed/ratio tradeoff and is used only
// when ctype is HdrFlagCompressLZX; the XPRESS and LZMS encoders have no
// equivalent knobs, so their calls are unchanged. Its zero value is
// lzx.Compress's exact behavior (lzx.CompressWith(data, lzx.Options{}) is
// byte-for-byte lzx.Compress(data), which that package pins with a test),
// so a zero-valued lzxOpts reproduces this function's pre-Options output
// exactly.
func compressChunk(ctype CompressionType, data []byte, lzxOpts lzx.Options) ([]byte, error) {
	switch ctype {
	case HdrFlagCompressXPRESS, HdrFlagCompressXPRESS2:
		return xpress.Compress(data), nil
	case HdrFlagCompressLZX:
		return lzx.CompressWith(data, lzxOpts), nil
	case HdrFlagCompressLZMS:
		return lzms.Compress(data), nil
	default:
		return nil, fmt.Errorf("wim: compress: unrecognized compression type %#x", ctype)
	}
}

// compressChunksParallel compresses every chunk of data (numChunks chunks of
// chunkSize bytes each, the last getting uncompressedSize's remainder) with
// compressionType, applying the same per-chunk raw-storage fallback
// EncodeResourceData documents. Each chunk is compressed completely
// independently by the WIM chunk format's own design (no shared Huffman
// table or window state crosses a chunk boundary -- see the lzx/lzms/xpress
// package docs), so chunks[i] is written by exactly one goroutine and
// needs no locking; a bounded worker pool (min(numChunks, GOMAXPROCS))
// keeps this from spawning thousands of goroutines for a huge resource
// while still saturating available cores. See TODO.md's "Performance:
// concurrency opportunities" entry for why this loop specifically was
// worth parallelizing (found slow against a real ~4GB WIM file's largest
// resources during a nano11-style debloat run, 2026-07-14).
func compressChunksParallel(data []byte, compressionType CompressionType, chunkSize uint32, uncompressedSize uint64, numChunks uint64, lzxOpts lzx.Options) ([][]byte, error) {
	chunks := make([][]byte, numChunks)

	workers := uint64(runtime.GOMAXPROCS(0))
	if workers > numChunks {
		workers = numChunks
	}
	if workers < 1 {
		workers = 1
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		next     uint64
	)
	for w := uint64(0); w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				// A plain atomic increment (rather than mu) to claim
				// the next chunk index: this is the only thing that
				// needs synchronizing per chunk, and with many chunks
				// across many workers a shared mutex here becomes real
				// contention that a lock-free counter avoids.
				i := atomic.AddUint64(&next, 1) - 1
				if i >= numChunks {
					return
				}

				start := i * uint64(chunkSize)
				usize := chunkUncompressedSize(i, numChunks, uncompressedSize, chunkSize)
				raw := data[start : start+usize]

				compressed, cerr := compressChunk(compressionType, raw, lzxOpts)
				if cerr != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("wim: encode resource: chunk %d: %w", i, cerr)
					}
					mu.Unlock()
					return
				}
				if uint64(len(compressed)) >= usize {
					// Compression did not shrink this chunk; store it raw.
					chunks[i] = raw
				} else {
					chunks[i] = compressed
				}
			}
		}()
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	return chunks, nil
}

// assembleCompressedPayload builds the chunk-table-framed payload (or, for a
// single chunk, the bare chunk) from chunks, exactly as
// EncodeResourceDataWith writes it. uncompressedSize is only used to size
// the chunk table's entries (chunkTableEntrySize), never to size the
// returned buffer's capacity: the returned slice's backing array is
// allocated at exactly len(table) + the sum of every chunks[i]'s length --
// the true final size -- rather than at len(table) + uncompressedSize
// (which is the UNcompressed total, and can be several times larger than
// the actual output for well-compressing data). This is split out from
// EncodeResourceDataWith mainly so a test can inspect cap() of the result
// directly to confirm the allocation is exact and no append call inside
// this function ever has to grow (and reallocate/copy) the backing array.
func assembleCompressedPayload(chunks [][]byte, uncompressedSize uint64) []byte {
	numChunks := uint64(len(chunks))
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
		// off already accumulated the compressed length of every chunk but
		// the last one while the table was being built above; add the last
		// chunk's length to get the exact final compressed total, instead
		// of over-allocating for uncompressedSize.
		totalCompressed := off + uint64(len(chunks[numChunks-1]))
		out := make([]byte, 0, len(table)+int(totalCompressed))
		out = append(out, table...)
		for _, c := range chunks {
			out = append(out, c...)
		}
		return out
	}
	out := make([]byte, 0, len(chunks[0]))
	out = append(out, chunks[0]...)
	return out
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
	return EncodeResourceDataWith(data, compressionType, chunkSize, lzx.Options{})
}

// EncodeResourceDataWith is EncodeResourceData with the LZX encoder's
// speed/compression-ratio tunables exposed, mirroring the
// lzx.Compress/lzx.CompressWith pair this package's LZX encoder itself
// offers.
//
// lzxOpts is used only when compressionType is HdrFlagCompressLZX: the
// XPRESS and LZMS encoders have no equivalent knobs and are called exactly
// as before. Its zero value is the LZX package's defaults, so
// EncodeResourceDataWith(data, ctype, chunkSize, lzx.Options{}) is
// byte-for-byte EncodeResourceData(data, ctype, chunkSize) -- which is what
// lets EncodeResourceData stay as-is rather than being a breaking signature
// change, and what makes WriteOptions.LZXOptions's zero value preserve
// every existing caller's exact output.
//
// The knobs matter a lot at WIM scale, because a WIM export re-encodes
// every blob: measured 2026-08-18 on a 24-core x86-64 machine over 29.4 MiB
// of real Windows install-image data compressed in 32 KiB chunks across all
// cores (see lzx.Options's own doc for the corpus and full ladder),
// lzx.Fast() runs at 20.5 MB/s for 1.75% larger output where the defaults
// run at 0.63 MB/s, and lzx.Balanced() at 3.2 MB/s for 0.66% larger output.
// Projected onto a real 7.4 GB install.wim re-export that is well under an
// hour versus most of a day of compression time, so callers re-encoding multi-gigabyte
// images should choose a preset deliberately rather than inheriting the
// ratio-first default.
func EncodeResourceDataWith(data []byte, compressionType CompressionType, chunkSize uint32, lzxOpts lzx.Options) (payload []byte, flags uint8, err error) {
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

	chunks, err := compressChunksParallel(data, compressionType, chunkSize, uncompressedSize, numChunks, lzxOpts)
	if err != nil {
		return nil, 0, err
	}

	out := assembleCompressedPayload(chunks, uncompressedSize)

	if uint64(len(out)) >= uncompressedSize {
		// Whole-resource fallback: compression didn't help overall (or, for
		// the single-chunk case, the one chunk was stored raw and this is
		// already byte-identical to the plain uncompressed form).
		return data, 0, nil
	}
	return out, ResFlagCompressed, nil
}

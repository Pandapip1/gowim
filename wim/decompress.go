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

// minParallelDecodeChunks is the smallest chunk count at which
// DecodeResourceData bothers spinning up a worker pool. Below this, the
// goroutine/WaitGroup/atomic-counter overhead is not worth it: with 1-2
// chunks there is at most one other chunk that could ever run concurrently
// with the first, so the serial loop already does effectively the same
// work with none of the synchronization cost. compressChunksParallel (the
// encode-side counterpart this mirrors) has no such threshold -- it always
// spins up min(numChunks, GOMAXPROCS) workers -- but its worker-pool setup
// is the same handful of cheap operations either way; the same reasoning
// that makes a threshold merely unnecessary there makes it cheap insurance
// here too, so it's included explicitly.
const minParallelDecodeChunks = 4

// decompressChunkInto decompresses one chunk's compressed bytes with the
// codec selected by ctype (one of the HdrFlagCompress* constants) directly
// into dst, whose length is the chunk's known uncompressed size. All three
// codecs are pure, WIM-agnostic single-buffer codecs (see their package
// docs) that decode entirely in terms of offsets relative to the start of
// their own output buffer (including each codec's internal post-processing
// filter, which -- like the LZ matching itself -- operates purely on
// position within that buffer); handing each one this chunk's own
// sub-slice of the resource's output buffer as dst is therefore equivalent
// to decoding into a freestanding chunk-sized buffer and copying it into
// place. This is the only place in this package that calls into them for
// decoding.
func decompressChunkInto(dst []byte, ctype CompressionType, data []byte) error {
	switch ctype {
	case HdrFlagCompressXPRESS, HdrFlagCompressXPRESS2:
		return xpress.DecompressInto(dst, data)
	case HdrFlagCompressLZX:
		return lzx.DecompressInto(dst, data)
	case HdrFlagCompressLZMS:
		return lzms.DecompressInto(dst, data)
	default:
		return fmt.Errorf("wim: decompress: unrecognized compression type %#x", ctype)
	}
}

// DecodeResourceData decodes the on-disk payload of a non-solid compressed
// resource (payload, the resource's raw SizeInWIM bytes as read from the
// file) into its full uncompressed contents, given the WIM's compression
// type, chunk size, and the resource's UncompressedSize.
//
// This implements the "original resource format" chunk-table framing
// documented in wimlib's src/resource.c: if uncompressedSize fits in a single
// chunkSize-sized chunk, payload is exactly one chunk with no chunk table.
// Otherwise payload begins with a table of ceil(uncompressedSize/chunkSize)-1
// little-endian offsets (4 bytes each if uncompressedSize <= 0xFFFFFFFF, else
// 8 bytes each), each giving a subsequent chunk's start offset relative to the
// byte immediately after the table (the first chunk's offset, 0, is implicit
// and has no table entry); chunk data follows. Each chunk's uncompressed
// length is chunkSize, except the last chunk which gets the remainder. A
// chunk whose stored (compressed) length equals its own uncompressed length
// is stored raw rather than run through the codec -- compression did not
// shrink that particular chunk.
//
// It is the caller's responsibility to ensure the resource is not solid
// (ResFlagSolid); solid resources use a different, out-of-scope framing (see
// wim.BlobTable.SolidResourceRun) and are not handled here.
func DecodeResourceData(payload []byte, ctype CompressionType, chunkSize uint32, uncompressedSize uint64) ([]byte, error) {
	if uncompressedSize == 0 {
		return nil, nil
	}
	if chunkSize == 0 {
		return nil, fmt.Errorf("wim: decode resource: chunk size is zero but resource is compressed")
	}
	numChunks := numChunksFor(uncompressedSize, chunkSize)

	var offsets []uint64 // offsets[i] = start offset of chunk i's compressed bytes, relative to end of table
	var chunksStart int  // offset within payload where chunk data begins (0 if no table)

	if numChunks > 1 {
		entrySize := chunkTableEntrySize(uncompressedSize)
		tableLen := int(numChunks-1) * entrySize
		if tableLen > len(payload) {
			return nil, fmt.Errorf("wim: decode resource: chunk table (%d bytes) exceeds payload (%d bytes)", tableLen, len(payload))
		}
		offsets = make([]uint64, numChunks)
		offsets[0] = 0
		for i := uint64(0); i < numChunks-1; i++ {
			off := int(i) * entrySize
			var v uint64
			if entrySize == 4 {
				v = uint64(le.Uint32(payload[off : off+4]))
			} else {
				v = le.Uint64(payload[off : off+8])
			}
			offsets[i+1] = v
		}
		chunksStart = tableLen
	} else {
		offsets = []uint64{0}
	}

	out := make([]byte, uncompressedSize)

	// Each chunk's start offset within out is fully determined up front by
	// the chunk table plus chunkSize -- chunkPos below is a running total of
	// prior chunks' uncompressed sizes, which (unlike the compressed-side
	// offsets table) never depends on any other chunk's actual decoded
	// bytes. That means, unlike compressChunksParallel (whose chunks[i]
	// entries hold compressed sizes that are unknown until that chunk is
	// actually compressed), every chunk here can be decoded directly into
	// its final position in out with no serial accumulator and no
	// reduction/copy step afterward: chunk i's bounds in payload (from
	// offsets) and its bounds in out (from chunkPos/usize) are both known
	// before any chunk is touched.
	chunkPos := make([]int, numChunks)
	chunkBounds := make([][2]int, numChunks) // [start,end) within payload
	pos := 0
	for i := uint64(0); i < numChunks; i++ {
		start := chunksStart + int(offsets[i])
		var end int
		if i+1 < numChunks {
			end = chunksStart + int(offsets[i+1])
		} else {
			end = len(payload)
		}
		if start < 0 || end > len(payload) || start > end {
			return nil, fmt.Errorf("wim: decode resource: chunk %d has invalid bounds [%d,%d) in payload of length %d", i, start, end, len(payload))
		}
		chunkBounds[i] = [2]int{start, end}
		usize := int(chunkUncompressedSize(i, numChunks, uncompressedSize, chunkSize))
		chunkPos[i] = pos
		pos += usize
	}

	decodeChunk := func(i uint64) error {
		b := chunkBounds[i]
		chunkData := payload[b[0]:b[1]]
		usize := int(chunkUncompressedSize(i, numChunks, uncompressedSize, chunkSize))
		dst := out[chunkPos[i] : chunkPos[i]+usize]

		if len(chunkData) == usize {
			// Stored raw: compression did not shrink this chunk.
			copy(dst, chunkData)
			return nil
		}
		if err := decompressChunkInto(dst, ctype, chunkData); err != nil {
			return fmt.Errorf("wim: decode resource: chunk %d: %w", i, err)
		}
		return nil
	}

	if numChunks < minParallelDecodeChunks {
		for i := uint64(0); i < numChunks; i++ {
			if err := decodeChunk(i); err != nil {
				return nil, err
			}
		}
		return out, nil
	}

	// Parallel path: each chunk decodes independently straight into its own
	// disjoint sub-slice of out (see decompressChunkInto's doc comment and
	// this function's own doc comment above), so a bounded worker pool
	// pulling chunk indices off a lock-free counter -- the same shape as
	// compressChunksParallel in compress.go -- needs no reduction step:
	// workers just write into out and are done.
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
				i := atomic.AddUint64(&next, 1) - 1
				if i >= numChunks {
					return
				}
				if err := decodeChunk(i); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

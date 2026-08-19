package wim

import (
	"fmt"
	"runtime"

	"github.com/Pandapip1/gowim/lzx"
)

// encodedBlob is one blob's compressed-and-framed result, produced by
// encodeBlobsPipeline and consumed strictly in blob-table order by WriteTo.
type encodedBlob struct {
	payload []byte
	flags   uint8
	dataLen int
	err     error
}

// encodeBlobsPipeline fetches and compresses every entry in bt.Entries
// concurrently (bounded by GOMAXPROCS workers), returning one
// result channel per entry that WriteTo drains strictly in order.
//
// Fetching+compressing a blob is entirely independent of every other blob
// (each is its own resource; see EncodeResourceData/compressChunksParallel
// for the same independence at the chunk level within one blob), so this is
// safe to parallelize; only the final byte-for-byte write to the output
// file must happen in blob-table order, which WriteTo still enforces by
// draining these channels in index order.
//
// Work is dispatched through a bounded jobs channel (capacity 2*workers)
// rather than letting every worker race ahead against a shared counter, so
// that at most a small, bounded number of blobs' compressed bytes are ever
// held in memory at once (each per-entry channel is itself buffered to
// exactly 1) -- important since a real WIM's total content can be many
// gigabytes, far more than is comfortable to hold in memory all at once
// (see BlobSource's own doc comment on this same concern).
// lzxOpts is threaded straight through to EncodeResourceDataWith, so
// WriteOptions.LZXOptions reaches every blob's chunks and not just the
// per-image metadata resources WriteTo encodes inline; its zero value is
// the LZX package's defaults (see WriteOptions.LZXOptions).
func encodeBlobsPipeline(bt *BlobTable, blobs BlobSource, compressionType CompressionType, chunkSize uint32, lzxOpts lzx.Options) []chan encodedBlob {
	n := len(bt.Entries)
	results := make([]chan encodedBlob, n)
	for i := range results {
		results[i] = make(chan encodedBlob, 1)
	}
	if n == 0 {
		return results
	}

	workers := runtime.GOMAXPROCS(0)
	if workers > n {
		workers = n
	}
	if workers < 1 {
		workers = 1
	}

	jobs := make(chan int, workers*2)
	go func() {
		defer close(jobs)
		for i := 0; i < n; i++ {
			jobs <- i
		}
	}()

	for w := 0; w < workers; w++ {
		go func() {
			for i := range jobs {
				hash := bt.Entries[i].Hash
				data, err := blobs.Blob(hash)
				if err != nil {
					results[i] <- encodedBlob{err: fmt.Errorf("blob %s: %w", hash, err)}
					continue
				}
				payload, flags, err := EncodeResourceDataWith(data, compressionType, chunkSize, lzxOpts)
				if err != nil {
					results[i] <- encodedBlob{err: fmt.Errorf("encode blob %s: %w", hash, err)}
					continue
				}
				results[i] <- encodedBlob{payload: payload, flags: flags, dataLen: len(data)}
			}
		}()
	}

	return results
}

package wim

import (
	"math/rand"
	"testing"
)

// newBenchBlobTable builds a synthetic BlobTable with n entries at the scale
// the real-image audit measured against (install.wim: 95,219 blob entries).
// lookupHashes returns m hashes drawn from the table (with replacement) in a
// fixed, seeded order suitable for repeatable benchmarking of hit lookups.
func newBenchBlobTable(n int) (*BlobTable, []Hash) {
	bt := &BlobTable{Entries: make([]BlobDescriptor, n)}
	for i := 0; i < n; i++ {
		bt.Entries[i] = BlobDescriptor{Hash: hashOf(i), RefCount: uint32(i)}
	}
	rng := rand.New(rand.NewSource(1))
	lookups := make([]Hash, 4096)
	for i := range lookups {
		lookups[i] = hashOf(rng.Intn(n))
	}
	return bt, lookups
}

// BenchmarkByHashLinearScan measures the pre-index O(n) implementation
// (byHashLinearScan, retained only for this A/B comparison) at realistic
// scale (95,219 entries, matching the real install.wim the audit measured).
func BenchmarkByHashLinearScan(b *testing.B) {
	const n = 95219
	bt, lookups := newBenchBlobTable(n)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bt.byHashLinearScan(lookups[i%len(lookups)])
	}
}

// BenchmarkByHashIndexed measures the current, index-backed O(1)
// implementation at the same scale.
func BenchmarkByHashIndexed(b *testing.B) {
	const n = 95219
	bt, lookups := newBenchBlobTable(n)
	// Prime the index once, outside the timed loop, mirroring real usage
	// where the first lookup pays the one-time build cost and every
	// subsequent one of the tens of thousands of per-file lookups does not.
	bt.ByHash(lookups[0])
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bt.ByHash(lookups[i%len(lookups)])
	}
}

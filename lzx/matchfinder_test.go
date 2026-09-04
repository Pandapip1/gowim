package lzx

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The tests and benchmarks in this file exist because the knobs added
// alongside them (Options.HashChainMatcher and Options.FullFirstPass -- see
// options.go) deliberately do NOT produce the same bytes as the parser they
// replace. Every earlier match-finder change in this package could be
// checked by asserting byte-identical output; these cannot, so what has to
// be checked instead is exactly the two properties a caller actually
// depends on:
//
//   - determinism (TestMatchFinderVariantsAreDeterministic): same input,
//     same Options, same bytes, every run. This is not a formality -- it is
//     what makes a WIM built twice from the same tree byte-identical.
//   - ratio (TestMatchFinderVariantRatios): the variant must not cost
//     meaningfully more output than the structure it replaces, measured
//     over a corpus rather than argued from first principles.
//
// plus the property no match-finder change may ever cost
// (TestMatchFinderVariantsRoundTrip): whatever match was chosen, the chunk
// still decodes back to exactly what went in.

// matchFinderCorpus returns the buffers these tests measure over, split
// into 32 KiB chunks -- the way the wim package actually drives this
// encoder, so that both the size totals and the benchmark timings below
// describe the real workload rather than one artificially large window.
//
// testdata/ supplies the real content: three captured chunks, one PE-heavy,
// one already-compressed, and one that was captured precisely because its
// statistics broke an earlier version of this encoder. The synthetic
// buffers then cover the shapes those three under-represent: pure noise
// (incompressible), long runs (where every match is capped and
// rediscovered), x86-like code (the E8 filter's case), record-structured
// binary (many short, near-in-distance matches, which is what the
// 3-vs-4-byte hash question is really about), and two flavors of text
// (prose-like and code-like), which the binary-heavy testdata has none of.
//
// Everything here is either a checked-in file or generated from a fixed
// seed, and deliberately nothing here is read from this package's own
// source tree. An earlier draft concatenated the package's .go files as its
// "real text" sample, which quietly made every size total in this file a
// function of the edits being measured -- two totals taken before and after
// a change were then not comparable at all, which is the one thing this
// corpus exists to make possible.
func matchFinderCorpus(tb testing.TB) []([]byte) {
	tb.Helper()

	var bufs [][]byte
	add := func(b []byte) {
		for off := 0; off < len(b); off += 32768 {
			end := off + 32768
			if end > len(b) {
				end = len(b)
			}
			if end-off >= 4096 { // ignore slivers: they measure startup, not matching
				bufs = append(bufs, b[off:end])
			}
		}
	}

	for _, name := range []string{"hash2_greedy_chunk1.bin", "real_uncompressed_block_chunk_plain.bin", "real_uncompressed_block_chunk_compressed.bin"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			tb.Fatalf("reading testdata/%s: %v", name, err)
		}
		add(data)
	}

	add(synthNoise(65536))
	add(synthRuns(65536))
	add(x86CodeLike(65536))
	add(synthRecords(65536))
	add(synthText(98304))
	add(synthCode(131072))

	return bufs
}

// lcg is the deterministic pseudorandom source the synthetic generators
// below share: a plain 32-bit LCG rather than math/rand, so that neither
// the corpus nor any size number measured over it depends on the standard
// library's generator staying stable across Go releases.
type lcg uint32

func (r *lcg) next() uint32 {
	*r = *r*1664525 + 1013904223
	return uint32(*r)
}

func (r *lcg) intn(n int) int { return int(r.next()>>8) % n }

func synthNoise(n int) []byte {
	r := lcg(1)
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.next() >> 24)
	}
	return b
}

// synthRuns is long runs of a repeated byte interrupted by short random
// stretches: every match is length-capped and immediately rediscovered as a
// repeat-offset match, which is the shape that makes match-finder descents
// deepest and therefore the shape prefix skipping is meant to help most.
func synthRuns(n int) []byte {
	r := lcg(2)
	b := make([]byte, 0, n)
	for len(b) < n {
		if r.intn(4) == 0 {
			for i := 0; i < r.intn(64)+1; i++ {
				b = append(b, byte(r.next()>>24))
			}
			continue
		}
		c := byte(r.intn(256))
		for i := 0; i < r.intn(2000)+16; i++ {
			b = append(b, c)
		}
	}
	return b[:n]
}

// synthRecords is fixed-width records with a few varying fields: dense in
// short, near matches, which is the content shape where a 4-byte bucket
// hash's inability to find a 3-byte match should cost the most.
func synthRecords(n int) []byte {
	r := lcg(3)
	b := make([]byte, 0, n)
	for len(b) < n {
		rec := make([]byte, 32)
		copy(rec, []byte("REC\x00\x01\x00\x00\x00"))
		for i := 8; i < 32; i++ {
			rec[i] = byte(0x20 + r.intn(8))
		}
		rec[12] = byte(r.next() >> 24)
		rec[13] = byte(r.next() >> 24)
		b = append(b, rec...)
	}
	return b[:n]
}

// synthText is word-salad from a small vocabulary: highly redundant at the
// 3-8 byte scale, which is where text compression actually lives, and a
// content type this package's testdata (PE binaries and a hive) has almost
// none of.
func synthText(n int) []byte {
	words := []string{
		"the", "compress", "match", "offset", "window", "length", "chunk", "literal",
		"table", "block", "aligned", "verbatim", "huffman", "encoder", "decoder", "of",
		"and", "a", "position", "candidate", "repeat", "queue", "tree", "chain",
	}
	r := lcg(4)
	b := make([]byte, 0, n)
	for len(b) < n {
		b = append(b, words[r.intn(len(words))]...)
		switch r.intn(8) {
		case 0:
			b = append(b, ".\n"...)
		case 1:
			b = append(b, ",\n    "...)
		default:
			b = append(b, ' ')
		}
	}
	return b[:n]
}

// synthCode is source-code-like text: indentation, punctuation-dense
// lines, and a small vocabulary of identifiers reused constantly at
// medium distances -- the content type whose redundancy sits at exactly
// the scale the match finder's bucket hash decides whether to see.
func synthCode(n int) []byte {
	idents := []string{"data", "pos", "limit", "bestLen", "bestOff", "cur", "head", "sons", "prev", "hash", "depth", "chunk", "model", "queue", "token", "offset"}
	forms := []string{
		"\tfor %s := 0; %s < %s; %s++ {\n",
		"\t\tif %s > %s {\n\t\t\t%s = %s\n\t\t}\n",
		"\t%s := %s(%s, %s)\n",
		"\t\t%s = append(%s, %s)\n",
		"\t// %s is the %s of %s\n",
		"\treturn %s, %s\n}\n\n",
	}
	r := lcg(5)
	b := make([]byte, 0, n)
	for len(b) < n {
		f := forms[r.intn(len(forms))]
		for i := 0; i < len(f); i++ {
			if f[i] == '%' && i+1 < len(f) && f[i+1] == 's' {
				b = append(b, idents[r.intn(len(idents))]...)
				i++
				continue
			}
			b = append(b, f[i])
		}
	}
	return b[:n]
}

// matchFinderVariants is the set of match-finder configurations measured
// below, each named for what it changes relative to "bst3_16" -- which is
// exactly what the Fast preset ships: a binary tree over a 3-byte bucket
// hash at search depth 16. Every variant sets DisableDP, because that is
// where these knobs actually apply -- see Options.HashChainMatcher's own
// doc for why enabling the DP would dilute the measurement rather than
// broaden it.
//
// The sweep in Fastest's doc (options.go) is this list; keep the two in
// step, so that re-running the benchmark below is enough to check whether
// the numbers quoted there still hold on a given machine.
func matchFinderVariants() []struct {
	name string
	opts Options
} {
	base := func(mut func(*Options)) Options {
		o := Options{DisableDP: true, MaxChainLen: 16, RefinePatience: 1} // Fast's other knobs
		mut(&o)
		return o
	}
	return []struct {
		name string
		opts Options
	}{
		{"bst3_16", base(func(o *Options) {})},
		{"chain3_16", base(func(o *Options) { o.HashChainMatcher = true })},
		{"chain3_32", base(func(o *Options) { o.HashChainMatcher = true; o.MaxChainLen = 32 })},
		// chain3_48 is the structure and depth Fastest ships (its doc has
		// the sweep this came out of): a chain deep enough to buy most of
		// the tree's ratio back, still well short of the tree's cost.
		{"chain3_48", base(func(o *Options) { o.HashChainMatcher = true; o.MaxChainLen = 48 })},
		{"chain3_96", base(func(o *Options) { o.HashChainMatcher = true; o.MaxChainLen = 96 })},
		// The greedy first pass is orthogonal to the structure, but it is
		// measured here because it is the other thing Fastest sets and
		// because pass 1 runs on every rung of the ladder.
		// ..._full1 opts back out of the default greedy first pass, so each
		// of these rows and its plain counterpart above together measure
		// what pass 1's parse quality is actually worth in final output
		// size (see Options.FullFirstPass).
		{"chain3_48_full1", base(func(o *Options) { o.HashChainMatcher = true; o.MaxChainLen = 48; o.FullFirstPass = true })},
		{"bst3_16_full1", base(func(o *Options) { o.FullFirstPass = true })},
	}
}

// TestMatchFinderVariantsAreDeterministic is the hard requirement on every
// one of these knobs: whatever match a variant picks, it must pick the same
// one every time. Five runs, byte-compared, over the whole corpus. A
// failure here is a bug in the match finder (an uninitialized read, a map
// iteration, a data race), never something to characterize and move on
// from.
func TestMatchFinderVariantsAreDeterministic(t *testing.T) {
	if testing.Short() {
		t.Skip("compresses the whole corpus 5x per variant")
	}
	corpus := matchFinderCorpus(t)
	for _, v := range matchFinderVariants() {
		for ci, chunk := range corpus {
			want := CompressWith(chunk, v.opts)
			for run := 1; run < 5; run++ {
				if got := CompressWith(chunk, v.opts); !bytes.Equal(got, want) {
					t.Fatalf("%s: chunk %d run %d differs from run 0 (%d vs %d bytes)", v.name, ci, run, len(got), len(want))
				}
			}
		}
	}
}

// TestMatchFinderVariantsRoundTrip is the correctness floor: whichever
// structure found the match, the chunk must still decode back to exactly
// what went in.
func TestMatchFinderVariantsRoundTrip(t *testing.T) {
	corpus := matchFinderCorpus(t)
	for _, v := range matchFinderVariants() {
		for ci, chunk := range corpus {
			out := CompressWith(chunk, v.opts)
			got, err := Decompress(out, len(chunk))
			if err != nil {
				t.Fatalf("%s: chunk %d: Decompress: %v", v.name, ci, err)
			}
			if !bytes.Equal(got, chunk) {
				t.Fatalf("%s: chunk %d: round-trip mismatch", v.name, ci)
			}
		}
	}
}

// TestMatchFinderVariantRatios reports every variant's total compressed
// size over the corpus as a percentage of the historical structure's, and
// fails only on a regression large enough that no plausible speedup would
// pay for it. The threshold is deliberately loose (5%): the point of this
// test is not to pin an exact size -- these knobs are allowed to change
// which match is chosen -- but to catch a variant that has silently stopped
// finding matches at all, and to keep the real numbers in front of whoever
// next changes this code. Run it with -v to see the table.
func TestMatchFinderVariantRatios(t *testing.T) {
	corpus := matchFinderCorpus(t)
	var raw int
	for _, c := range corpus {
		raw += len(c)
	}

	totals := map[string]int{}
	for _, v := range matchFinderVariants() {
		total := 0
		for _, chunk := range corpus {
			total += len(CompressWith(chunk, v.opts))
		}
		totals[v.name] = total
	}

	baseline := totals["bst3_16"]
	t.Logf("corpus: %d chunks, %d raw bytes", len(corpus), raw)
	for _, v := range matchFinderVariants() {
		got := totals[v.name]
		pct := 100 * float64(got-baseline) / float64(baseline)
		t.Logf("%-18s %9d bytes  %+.3f%% vs bst3_16", v.name, got, pct)
		if pct > 5 {
			t.Errorf("%s: %+.3f%% larger than bst3_16 (%d vs %d bytes) -- far past the point any speedup pays for", v.name, pct, got, baseline)
		}
	}
}

// TestGreedyFirstPassIsSizeNeutral is the claim Options.FullFirstPass's doc
// makes: parsing compress()'s throwaway first pass greedily, rather than
// with the full lookahead, does not measurably change what the real passes
// then produce. That claim is what licenses the greedy parse being the
// default on every rung of the ladder, including Default itself, so it is
// checked on every rung and on both of this package's corpora.
//
// The threshold is 0.25% in aggregate: the measurement it is guarding
// (2026-09-03) came in between -0.068% and +0.019% on eight
// corpus/preset combinations, so anything past a quarter percent means the
// seed table has started to matter in a way it did not, which is a real
// finding and should fail rather than pass quietly.
func TestGreedyFirstPassIsSizeNeutral(t *testing.T) {
	if testing.Short() {
		t.Skip("compresses two corpora under every preset, twice")
	}
	// Named slices rather than a map, so the log reads in the same order
	// every run -- this file is about determinism, and its own output
	// should not be the exception.
	var other [][]byte
	for _, name := range []string{"hash2_greedy_chunk1.bin", "long_run", "mixed", "noise", "real_uncompressed_block_chunk_plain.bin"} {
		other = append(other, optionsTestCorpora(t)[name])
	}
	corpora := []struct {
		name string
		bufs [][]byte
	}{{"matchfinder", matchFinderCorpus(t)}, {"options", other}}

	for _, p := range []struct {
		name string
		opts Options
	}{{"Fast", Fast()}, {"Fastest", Fastest()}, {"Balanced", Balanced()}, {"Default", DefaultOptions()}, {"Max", Max()}} {
		with, without := p.opts, p.opts
		with.FullFirstPass = false
		without.FullFirstPass = true
		for _, corpus := range corpora {
			a, b := 0, 0
			for _, c := range corpus.bufs {
				a += len(CompressWith(c, without))
				b += len(CompressWith(c, with))
			}
			pct := 100 * float64(b-a) / float64(a)
			t.Logf("%-11s %-8s full %8d  greedy %8d  %+.3f%%", corpus.name, p.name, a, b, pct)
			if pct > 0.25 {
				t.Errorf("%s/%s: a greedy first pass cost %+.3f%% of output (%d vs %d bytes); it is documented as size-neutral", corpus.name, p.name, pct, b, a)
			}
		}
	}
}

// BenchmarkMatchFinderVariants times each variant over the whole corpus,
// reporting MB/s of input. Relative numbers between variants in a single
// run are the meaningful output; absolute throughput depends on the machine
// and on what else is running on it.
func BenchmarkMatchFinderVariants(b *testing.B) {
	corpus := matchFinderCorpus(b)
	var raw int64
	for _, c := range corpus {
		raw += int64(len(c))
	}
	for _, v := range matchFinderVariants() {
		b.Run(v.name, func(b *testing.B) {
			b.SetBytes(raw)
			for i := 0; i < b.N; i++ {
				for _, chunk := range corpus {
					CompressWith(chunk, v.opts)
				}
			}
		})
	}
}

// BenchmarkFastPresets times Fast and Fastest exactly as a caller gets
// them, so that a change to either preset's own defaults shows up here
// without having to be restated as a variant above. The ratio between these
// two is the number Fastest's doc quotes.
func BenchmarkFastPresets(b *testing.B) {
	corpus := matchFinderCorpus(b)
	var raw int64
	for _, c := range corpus {
		raw += int64(len(c))
	}
	for _, p := range []struct {
		name string
		opts Options
	}{{"Fast", Fast()}, {"Fastest", Fastest()}} {
		b.Run(p.name, func(b *testing.B) {
			b.SetBytes(raw)
			for i := 0; i < b.N; i++ {
				for _, chunk := range corpus {
					CompressWith(chunk, p.opts)
				}
			}
		})
	}
}

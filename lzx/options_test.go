package lzx

import (
	"bytes"
	"os"
	"testing"
)

// optionsTestCorpora returns the byte buffers the Options tests below run
// every preset over: this package's two real captured chunks (a real WIM
// chunk, and the chunk that first exposed the hash2 greedy-splice bug),
// plus small synthetic buffers exercising the degenerate shapes the knobs
// interact with (a long run, which is what repeat-length sampling and beam
// width are about, and incompressible random-ish data, which is what the
// block-split trials are about).
func optionsTestCorpora(t *testing.T) map[string][]byte {
	t.Helper()
	corpora := map[string][]byte{}
	for _, name := range []string{"hash2_greedy_chunk1.bin", "real_uncompressed_block_chunk_plain.bin"} {
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatalf("reading testdata/%s: %v", name, err)
		}
		corpora[name] = data
	}

	run := bytes.Repeat([]byte("abcabcabc"), 900)
	corpora["long_run"] = run

	// Deterministic pseudorandom bytes: an LCG, not math/rand, so this test
	// does not depend on the standard library's generator staying stable.
	noise := make([]byte, 20000)
	x := uint32(12345)
	for i := range noise {
		x = x*1664525 + 1013904223
		noise[i] = byte(x >> 24)
	}
	corpora["noise"] = noise

	mixed := append(append([]byte{}, run[:5000]...), noise[:5000]...)
	corpora["mixed"] = mixed
	return corpora
}

// TestCompressWithZeroOptionsMatchesCompress pins the central promise of
// the Options API: the zero value is not merely "a good default", it is
// byte-for-byte what Compress already does. If a new knob is ever added
// whose zero value changes behavior, this fails.
func TestCompressWithZeroOptionsMatchesCompress(t *testing.T) {
	for name, data := range optionsTestCorpora(t) {
		want := Compress(data)
		got := CompressWith(data, Options{})
		if !bytes.Equal(got, want) {
			t.Errorf("%s: CompressWith(data, Options{}) differs from Compress(data) (%d vs %d bytes)", name, len(got), len(want))
		}
		if got := CompressWith(data, DefaultOptions()); !bytes.Equal(got, want) {
			t.Errorf("%s: CompressWith(data, DefaultOptions()) differs from Compress(data)", name)
		}
	}
}

// TestPresetsRoundTrip checks the property that actually matters about all
// these knobs: none of them changes the format. Every preset, and a few
// hand-set field combinations including deliberately out-of-range values,
// must still produce a chunk this package's own decoder restores exactly.
func TestPresetsRoundTrip(t *testing.T) {
	presets := map[string]Options{
		"Fast":     Fast(),
		"Balanced": Balanced(),
		"Default":  DefaultOptions(),
		"Max":      Max(),
		// Escape-hatch combinations, including values resolve() has to
		// clamp: a caller passing nonsense gets slow-or-bad compression,
		// never a panic or a corrupt chunk.
		"beam1":         {BeamWidth: 1},
		"negatives":     {BeamWidth: -5, MaxFreshCandidates: -1, MaxChainLen: -1, DPRepeatLengthSamples: -3, RefinePatience: -1, MaxRefineIters: -1},
		"reps_clamped":  {DPRepeatLengthSamples: 99},
		"nohash2":       {DisableDPHash2: true},
		"nosplit_nodp":  {DisableDPHash2: true, DisableDP: true, DisableBlockSplit: true},
		"one_iteration": {MaxRefineIters: 1, RefinePatience: 1},
	}
	for name, data := range optionsTestCorpora(t) {
		for pname, opts := range presets {
			out := CompressWith(data, opts)
			got, err := Decompress(out, len(data))
			if err != nil {
				t.Fatalf("%s/%s: Decompress: %v", name, pname, err)
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("%s/%s: round-trip mismatch", name, pname)
			}
		}
	}
}

// TestPresetLadderIsOrdered checks that the ladder is actually a ladder:
// each rung must not produce more total output than the faster rung above
// it. The check is on the TOTAL across the corpora, not per buffer, and
// deliberately so: these knobs change which parse the encoder picks rather
// than strictly widening a search, so a single buffer can invert -- e.g.
// on testdata/hash2_greedy_chunk1.bin alone, Fast's narrower MaxChainLen
// happens to find a parse 16 bytes smaller than the same run at the
// default search depth (measured 8872 vs 8888, 2026-08-18). The ladder's
// ordering is a
// statement about aggregate behavior, which is what a caller choosing a
// rung actually experiences, and the per-rung byte counts quoted in the
// preset docs are likewise corpus totals.
func TestPresetLadderIsOrdered(t *testing.T) {
	corpora := optionsTestCorpora(t)
	ladder := []struct {
		name string
		opts Options
	}{
		{"Fast", Fast()},
		{"Balanced", Balanced()},
		{"Default", DefaultOptions()},
		{"Max", Max()},
	}
	prev := 0
	prevName := ""
	for _, rung := range ladder {
		total := 0
		for _, data := range corpora {
			total += len(CompressWith(data, rung.opts))
		}
		if prev != 0 && total > prev {
			t.Errorf("%s encoded %d bytes total, more than the faster %s's %d", rung.name, total, prevName, prev)
		}
		t.Logf("%-9s %d bytes total", rung.name, total)
		prev, prevName = total, rung.name
	}
}

// TestOptionsResolveDefaults checks the zero-means-default mapping itself,
// including that each default matches the package constant it is documented
// to match -- so that changing a constant without updating Options' doc is
// caught here rather than silently making the doc wrong.
func TestOptionsResolveDefaults(t *testing.T) {
	got := Options{}.resolve()
	want := encodeOptions{
		dp:                  true,
		beamWidth:           defaultBeamWidth,
		maxFreshCandidates:  defaultMaxFreshCandidates,
		maxChainLen:         maxChainLen,
		repeatLengthSamples: 2,
		dpHash2:             true,
		refinePatience:      refinePatience,
		maxRefineIters:      maxRefineItersHardCap,
		blockSplit:          true,
	}
	if got != want {
		t.Errorf("Options{}.resolve() = %+v, want %+v", got, want)
	}

	if r := (Options{DPRepeatLengthSamples: 7}).resolve(); r.repeatLengthSamples != 2 {
		t.Errorf("DPRepeatLengthSamples 7 resolved to %d, want it clamped to 2", r.repeatLengthSamples)
	}
	if r := (Options{BeamWidth: -3, MaxChainLen: -1}).resolve(); r.beamWidth != 1 || r.maxChainLen != 1 {
		t.Errorf("negative knobs resolved to beamWidth=%d maxChainLen=%d, want both clamped to 1", r.beamWidth, r.maxChainLen)
	}
}

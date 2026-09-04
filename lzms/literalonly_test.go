package lzms

import (
	"bytes"
	"math/rand"
	"testing"
)

// roundTripWith is roundTrip but using CompressWith(data, opts) instead of
// Compress(data), so LiteralOnly's correctness can be checked the same way
// the default path already is in lzms_test.go.
func roundTripWith(t *testing.T, data []byte, opts Options) {
	t.Helper()
	compressed := CompressWith(data, opts)
	got, err := Decompress(compressed, len(data))
	if err != nil {
		t.Fatalf("Decompress failed: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("round trip mismatch with opts %+v: input len %d, output len %d", opts, len(data), len(got))
	}
}

func TestRoundTripLiteralOnly(t *testing.T) {
	opts := Options{LiteralOnly: true}

	roundTripWith(t, nil, opts)
	roundTripWith(t, []byte{0x42}, opts)
	roundTripWith(t, []byte("abcd"), opts)

	// Compressible data.
	roundTripWith(t, bytes.Repeat([]byte("ABCD"), 50000), opts)
	text := bytes.Repeat([]byte(
		"The quick brown fox jumps over the lazy dog. "+
			"Pack my box with five dozen liquor jugs.\n"), 2000)
	roundTripWith(t, text, opts)

	// Incompressible data.
	r := rand.New(rand.NewSource(1))
	random := make([]byte, 65536)
	r.Read(random)
	roundTripWith(t, random, opts)

	// Chunk-boundary sizes, mixed content.
	const chunkSize = 131072
	r2 := rand.New(rand.NewSource(2))
	for _, size := range []int{chunkSize - 1, chunkSize, chunkSize + 1} {
		data := make([]byte, size)
		r2.Read(data)
		for i := 1000; i < len(data); i++ {
			if i%7 == 0 {
				data[i] = data[i-997]
			}
		}
		roundTripWith(t, data, opts)
	}
}

// TestLiteralOnlyDoesNotChangeDefaultOutput pins that leaving LiteralOnly at
// its zero value (false) is byte-for-byte identical to today's Compress,
// across both the default Options{} path and CompressWith with other fields
// set -- i.e. adding this field does not change output for any existing
// caller.
func TestLiteralOnlyDoesNotChangeDefaultOutput(t *testing.T) {
	data := bytes.Repeat([]byte("The quick brown fox jumps over the lazy dog. "), 500)

	want := Compress(data)
	got := CompressWith(data, Options{})
	if !bytes.Equal(want, got) {
		t.Fatalf("CompressWith(data, Options{}) differs from Compress(data)")
	}
}

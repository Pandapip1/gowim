package wim

import (
	"bytes"
	"testing"
)

// TestDecodeResourceDataParallelMatchesSerialOracle checks the parallel
// DecodeResourceData against decodeResourceDataOld (a byte-for-byte
// reproduction of the pre-parallel, pre-DecompressInto implementation kept
// in decompress_bench_test.go as an independent oracle), across resource
// sizes both below and above minParallelDecodeChunks so both the serial
// small-resource path and the worker-pool path are exercised.
func TestDecodeResourceDataParallelMatchesSerialOracle(t *testing.T) {
	cases := []struct {
		name             string
		ctype            CompressionType
		chunkSize        uint32
		uncompressedSize uint64
	}{
		{"xpress below threshold", HdrFlagCompressXPRESS, 32768, 32768 * 2},
		{"xpress at threshold", HdrFlagCompressXPRESS, 32768, 32768 * 4},
		{"xpress above threshold", HdrFlagCompressXPRESS, 32768, 32768 * 40},
		{"lzx above threshold", HdrFlagCompressLZX, 32768, 32768 * 40},
		{"lzms above threshold", HdrFlagCompressLZMS, 1 << 20, (1 << 20) * 8},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			data := make([]byte, c.uncompressedSize)
			for i := range data {
				data[i] = byte(i*7 + i/97)
			}

			payload, _, err := EncodeResourceData(data, c.ctype, c.chunkSize)
			if err != nil {
				t.Fatalf("EncodeResourceData: %v", err)
			}

			want, err := decodeResourceDataOld(payload, c.ctype, c.chunkSize, c.uncompressedSize)
			if err != nil {
				t.Fatalf("decodeResourceDataOld: %v", err)
			}
			got, err := DecodeResourceData(payload, c.ctype, c.chunkSize, c.uncompressedSize)
			if err != nil {
				t.Fatalf("DecodeResourceData: %v", err)
			}
			if !bytes.Equal(want, got) {
				t.Fatalf("parallel decode mismatch vs serial oracle (want %d bytes, got %d bytes)", len(want), len(got))
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("decoded data does not match original input")
			}
		})
	}
}

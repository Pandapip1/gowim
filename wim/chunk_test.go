package wim

import (
	"bytes"
	"crypto/rand"
	"testing"
)

// synthPatterns returns a set of byte slices covering the edge cases called
// out in the task: empty, one chunk, several chunks, a partial final chunk,
// and data that doesn't compress at all.
func synthPatterns(t *testing.T, chunkSize uint32) map[string][]byte {
	t.Helper()
	cs := int(chunkSize)
	repeat := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(i % 251)
		}
		return b
	}
	random := func(n int) []byte {
		b := make([]byte, n)
		if _, err := rand.Read(b); err != nil {
			t.Fatalf("rand.Read: %v", err)
		}
		return b
	}
	return map[string][]byte{
		"under one chunk":        repeat(cs / 2),
		"exactly one chunk":      repeat(cs),
		"several full chunks":    repeat(cs * 3),
		"partial final chunk":    repeat(cs*2 + cs/3),
		"random incompressible":  random(cs*2 + 17),
		"single byte":            {0x42},
		"empty (skipped by ==0)": {},
	}
}

func TestEncodeDecodeResourceDataRoundTrip(t *testing.T) {
	types := map[string]struct {
		ctype     CompressionType
		chunkSize uint32
	}{
		"xpress": {HdrFlagCompressXPRESS, 32768},
		"lzx":    {HdrFlagCompressLZX, 32768},
		"lzms":   {HdrFlagCompressLZMS, 131072},
	}

	for typeName, tc := range types {
		for patName, data := range synthPatterns(t, tc.chunkSize) {
			data := data
			t.Run(typeName+"/"+patName, func(t *testing.T) {
				if len(data) == 0 {
					t.Skip("EncodeResourceData/DecodeResourceData are not called for zero-length resources; resourceData short-circuits on rh.IsZero()")
				}
				payload, flags, err := EncodeResourceData(data, tc.ctype, tc.chunkSize)
				if err != nil {
					t.Fatalf("EncodeResourceData: %v", err)
				}

				var got []byte
				if flags&ResFlagCompressed == 0 {
					if !bytes.Equal(payload, data) {
						t.Fatalf("uncompressed fallback payload does not match original data")
					}
					got = payload
				} else {
					got, err = DecodeResourceData(payload, tc.ctype, tc.chunkSize, uint64(len(data)))
					if err != nil {
						t.Fatalf("DecodeResourceData: %v", err)
					}
				}
				if !bytes.Equal(got, data) {
					t.Fatalf("round trip mismatch: got %d bytes, want %d bytes", len(got), len(data))
				}
			})
		}
	}
}

// TestEncodeResourceDataChunkTableFraming exercises a case with several
// chunks and checks the produced chunk table has the expected shape (right
// entry count/size, monotonically increasing offsets) in addition to round
// tripping.
func TestEncodeResourceDataChunkTableFraming(t *testing.T) {
	const chunkSize = 4096
	data := make([]byte, chunkSize*5+123)
	for i := range data {
		// Make each chunk's content different enough that identical
		// compressed chunks are not expected, without being incompressible.
		data[i] = byte((i / 7) % 200)
	}
	payload, flags, err := EncodeResourceData(data, HdrFlagCompressXPRESS, chunkSize)
	if err != nil {
		t.Fatalf("EncodeResourceData: %v", err)
	}
	if flags&ResFlagCompressed == 0 {
		t.Fatalf("expected compression to be used for compressible multi-chunk data")
	}
	numChunks := numChunksFor(uint64(len(data)), chunkSize)
	if numChunks != 6 {
		t.Fatalf("numChunks = %d, want 6", numChunks)
	}
	entrySize := chunkTableEntrySize(uint64(len(data)))
	if entrySize != 4 {
		t.Fatalf("entrySize = %d, want 4", entrySize)
	}
	tableLen := int(numChunks-1) * entrySize
	if len(payload) < tableLen {
		t.Fatalf("payload too short for chunk table: %d < %d", len(payload), tableLen)
	}
	var prev uint32
	for i := 0; i < int(numChunks-1); i++ {
		off := le.Uint32(payload[i*entrySize : i*entrySize+entrySize])
		if i > 0 && off < prev {
			t.Fatalf("chunk table offsets not monotonically increasing at entry %d: %d < %d", i, off, prev)
		}
		prev = off
	}

	got, err := DecodeResourceData(payload, HdrFlagCompressXPRESS, chunkSize, uint64(len(data)))
	if err != nil {
		t.Fatalf("DecodeResourceData: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("round trip mismatch")
	}
}

// TestEncodeResourceDataAllZero covers the specific "resource-level raw
// fallback" case for data that is trivially compressible per-chunk but where
// we still want to confirm the compressed flag is set (since all-zero data
// compresses very well, it should NOT trigger the raw fallback).
func TestEncodeResourceDataAllZero(t *testing.T) {
	data := make([]byte, 32768*3)
	payload, flags, err := EncodeResourceData(data, HdrFlagCompressLZX, 32768)
	if err != nil {
		t.Fatalf("EncodeResourceData: %v", err)
	}
	if flags&ResFlagCompressed == 0 {
		t.Fatalf("expected all-zero data to compress well enough to keep ResFlagCompressed set")
	}
	if len(payload) >= len(data) {
		t.Fatalf("expected compressed payload (%d bytes) to be smaller than original (%d bytes)", len(payload), len(data))
	}
	got, err := DecodeResourceData(payload, HdrFlagCompressLZX, 32768, uint64(len(data)))
	if err != nil {
		t.Fatalf("DecodeResourceData: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("round trip mismatch")
	}
}

// TestEncodeResourceDataIncompressibleFallsBackToRaw confirms the
// whole-resource raw-storage fallback: random data that doesn't compress at
// all (or barely) for a resource that fits in a single chunk should come back
// with flags == 0 and payload == data.
func TestEncodeResourceDataIncompressibleFallsBackToRaw(t *testing.T) {
	data := make([]byte, 4096)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	for _, ctype := range []CompressionType{HdrFlagCompressXPRESS, HdrFlagCompressLZX, HdrFlagCompressLZMS} {
		payload, flags, err := EncodeResourceData(data, ctype, 32768)
		if err != nil {
			t.Fatalf("EncodeResourceData(%#x): %v", ctype, err)
		}
		if flags != 0 {
			t.Fatalf("EncodeResourceData(%#x): flags = %#x, want 0 (single incompressible chunk should fall back to raw)", ctype, flags)
		}
		if !bytes.Equal(payload, data) {
			t.Fatalf("EncodeResourceData(%#x): payload does not equal original data in raw fallback", ctype)
		}
	}
}

func TestCompressionTypeFromHeaderFlags(t *testing.T) {
	tests := []struct {
		name    string
		flags   uint32
		want    CompressionType
		wantErr bool
	}{
		{"no compression flag", 0, CompressionNone, false},
		{"lzx", HdrFlagCompression | HdrFlagCompressLZX, HdrFlagCompressLZX, false},
		{"xpress", HdrFlagCompression | HdrFlagCompressXPRESS, HdrFlagCompressXPRESS, false},
		{"xpress2 maps to xpress", HdrFlagCompression | HdrFlagCompressXPRESS2, HdrFlagCompressXPRESS, false},
		{"lzms", HdrFlagCompression | HdrFlagCompressLZMS, HdrFlagCompressLZMS, false},
		{"lzx wins over xpress2 (mirrors wimlib's check order)", HdrFlagCompression | HdrFlagCompressLZX | HdrFlagCompressXPRESS2, HdrFlagCompressLZX, false},
		{"compression flag set but no type", HdrFlagCompression, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := Header{Flags: tc.flags}
			got, err := h.CompressionType()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("CompressionType() = %#x, nil, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("CompressionType(): %v", err)
			}
			if got != tc.want {
				t.Fatalf("CompressionType() = %#x, want %#x", got, tc.want)
			}
		})
	}
}

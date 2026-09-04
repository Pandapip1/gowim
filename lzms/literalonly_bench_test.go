package lzms

import (
	"bytes"
	"math/rand"
	"testing"
)

// compressibleCorpus returns ~32 KiB of highly compressible, text-like data
// (long repeated phrases), representative of the "matches are abundant"
// regime LiteralOnly is expected to lose on.
func compressibleCorpus() []byte {
	phrase := "The quick brown fox jumps over the lazy dog. " +
		"Pack my box with five dozen liquor jugs. " +
		"LZMS is a Microsoft compression format used by WIM and ESD files.\n"
	var buf bytes.Buffer
	for buf.Len() < 32*1024 {
		buf.WriteString(phrase)
	}
	return buf.Bytes()[:32*1024]
}

// randomCorpus returns ~32 KiB of incompressible random data, representative
// of the regime LiteralOnly is expected to win on.
func randomCorpus() []byte {
	r := rand.New(rand.NewSource(42))
	data := make([]byte, 32*1024)
	r.Read(data)
	return data
}

func BenchmarkEncodeCompressible_Default(b *testing.B) {
	data := compressibleCorpus()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	var out []byte
	for i := 0; i < b.N; i++ {
		out = CompressWith(data, Options{})
	}
	b.ReportMetric(float64(len(out)), "bytes/op-output")
}

func BenchmarkEncodeCompressible_LiteralOnly(b *testing.B) {
	data := compressibleCorpus()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	var out []byte
	for i := 0; i < b.N; i++ {
		out = CompressWith(data, Options{LiteralOnly: true})
	}
	b.ReportMetric(float64(len(out)), "bytes/op-output")
}

func BenchmarkEncodeRandom_Default(b *testing.B) {
	data := randomCorpus()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	var out []byte
	for i := 0; i < b.N; i++ {
		out = CompressWith(data, Options{})
	}
	b.ReportMetric(float64(len(out)), "bytes/op-output")
}

func BenchmarkEncodeRandom_LiteralOnly(b *testing.B) {
	data := randomCorpus()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	var out []byte
	for i := 0; i < b.N; i++ {
		out = CompressWith(data, Options{LiteralOnly: true})
	}
	b.ReportMetric(float64(len(out)), "bytes/op-output")
}

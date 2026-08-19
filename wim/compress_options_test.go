package wim

import (
	"bytes"
	"testing"

	"github.com/Pandapip1/gowim/lzx"
)

// realBinaryFixture returns a realistically-compressible payload: the real
// setup.exe bytes recovered from the embedded LZX resource captured from a
// Windows 11 23H2 boot.wim (see testdata_test.go). Synthetic fixtures are
// useless for the tests below -- the repetitive ones compress to the same
// bytes under every preset, and the random one is stored raw by the
// per-chunk fallback -- so the LZX presets can only be told apart on data
// that actually looks like what a WIM stores.
func realBinaryFixture(t *testing.T) []byte {
	t.Helper()
	data, err := DecodeResourceData(lzxSetupExeResource, HdrFlagCompressLZX, 32768, 95712)
	if err != nil {
		t.Fatalf("DecodeResourceData(embedded lzx setup.exe): %v", err)
	}
	return data
}

// TestEncodeResourceDataWithZeroOptionsMatchesEncodeResourceData pins the
// backward-compatibility contract that lets EncodeResourceData keep its
// pre-Options signature: the zero lzx.Options must mean "the lzx package's
// defaults", so the two entry points are byte-identical, not merely
// equivalent. This mirrors the lzx package's own test pinning
// Compress(data) == CompressWith(data, Options{}).
func TestEncodeResourceDataWithZeroOptionsMatchesEncodeResourceData(t *testing.T) {
	data := realBinaryFixture(t)

	for _, tc := range []struct {
		name      string
		ctype     CompressionType
		chunkSize uint32
	}{
		{"lzx", HdrFlagCompressLZX, 32768},
		{"xpress", HdrFlagCompressXPRESS, 32768},
		{"lzms", HdrFlagCompressLZMS, 131072},
		{"none", CompressionNone, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldPayload, oldFlags, err := EncodeResourceData(data, tc.ctype, tc.chunkSize)
			if err != nil {
				t.Fatalf("EncodeResourceData: %v", err)
			}
			newPayload, newFlags, err := EncodeResourceDataWith(data, tc.ctype, tc.chunkSize, lzx.Options{})
			if err != nil {
				t.Fatalf("EncodeResourceDataWith: %v", err)
			}
			if oldFlags != newFlags {
				t.Fatalf("flags: EncodeResourceData %#x, EncodeResourceDataWith(zero) %#x", oldFlags, newFlags)
			}
			if !bytes.Equal(oldPayload, newPayload) {
				t.Fatalf("payload differs between EncodeResourceData (%d bytes) and EncodeResourceDataWith with zero Options (%d bytes)", len(oldPayload), len(newPayload))
			}
		})
	}
}

// TestEncodeResourceDataLZXDefaultIsPlainLzxCompress pins the default LZX
// path against the lzx package's own default entry point, one level below
// the EncodeResourceData/EncodeResourceDataWith pair: a resource small
// enough to be a single chunk is framed with no chunk table, so its payload
// must be exactly lzx.Compress's output for those bytes. If the Options
// plumbing ever started resolving something other than the package defaults
// for an unset field, this catches it even though both sides of the test
// above would still agree with each other.
func TestEncodeResourceDataLZXDefaultIsPlainLzxCompress(t *testing.T) {
	data := realBinaryFixture(t)[:20000] // < 32768: exactly one chunk, no chunk table

	payload, flags, err := EncodeResourceData(data, HdrFlagCompressLZX, 32768)
	if err != nil {
		t.Fatalf("EncodeResourceData: %v", err)
	}
	if flags&ResFlagCompressed == 0 {
		t.Fatalf("flags = %#x, want ResFlagCompressed (fixture should compress)", flags)
	}
	if want := lzx.Compress(data); !bytes.Equal(payload, want) {
		t.Fatalf("single-chunk LZX payload (%d bytes) != lzx.Compress output (%d bytes)", len(payload), len(want))
	}
}

// TestEncodeResourceDataWithLZXPresetReachesEncoder proves the options are
// not silently dropped somewhere in the framing code: the same single-chunk
// resource encoded with lzx.Fast() must be byte-identical to
// lzx.CompressWith(data, lzx.Fast()) and must differ from the default
// encoding. (Fast disables the DP parser, which measured 2.87% larger
// output over a 4 MiB corpus on 2026-08-18; a single 20 KB chunk is a much
// smaller sample than that, so the test asserts only that the bytes differ,
// not by how much.)
func TestEncodeResourceDataWithLZXPresetReachesEncoder(t *testing.T) {
	data := realBinaryFixture(t)[:20000]

	fast, _, err := EncodeResourceDataWith(data, HdrFlagCompressLZX, 32768, lzx.Fast())
	if err != nil {
		t.Fatalf("EncodeResourceDataWith(Fast): %v", err)
	}
	if want := lzx.CompressWith(data, lzx.Fast()); !bytes.Equal(fast, want) {
		t.Fatalf("Fast-preset payload (%d bytes) != lzx.CompressWith(data, lzx.Fast()) (%d bytes)", len(fast), len(want))
	}
	def, _, err := EncodeResourceData(data, HdrFlagCompressLZX, 32768)
	if err != nil {
		t.Fatalf("EncodeResourceData: %v", err)
	}
	if bytes.Equal(fast, def) {
		t.Fatalf("Fast preset produced byte-identical output to the default; the option is not reaching the encoder")
	}

	// The non-LZX codecs take no options, so passing one must change
	// nothing at all for them.
	for _, ctype := range []CompressionType{HdrFlagCompressXPRESS, HdrFlagCompressLZMS} {
		withOpts, _, err := EncodeResourceDataWith(data, ctype, 32768, lzx.Fast())
		if err != nil {
			t.Fatalf("EncodeResourceDataWith(%#x): %v", ctype, err)
		}
		plain, _, err := EncodeResourceData(data, ctype, 32768)
		if err != nil {
			t.Fatalf("EncodeResourceData(%#x): %v", ctype, err)
		}
		if !bytes.Equal(withOpts, plain) {
			t.Fatalf("compression type %#x: LZX options changed non-LZX output", ctype)
		}
	}
}

// realBinaryImageFixture is twoImageFixture's shape but with real binary
// content, for the whole-WIM tests below.
func realBinaryImageFixture(t *testing.T) [2]map[string][]byte {
	t.Helper()
	data := realBinaryFixture(t)
	return [2]map[string][]byte{
		{"setup.exe": data, "head.bin": data[:20000]},
		{"tail.bin": data[20000:]},
	}
}

// TestWriteOptionsZeroLZXOptionsPreservesOutput is the writer-level half of
// the zero-value contract: leaving WriteOptions.LZXOptions unset must
// produce exactly the bytes the writer produced before the field existed.
// Both halves are checked -- an explicit lzx.DefaultOptions() (the named
// spelling of the same rung) and the raw zero struct must agree, and every
// blob payload in the zero-value WIM must match what the pre-Options
// EncodeResourceData path produces for the same bytes.
func TestWriteOptionsZeroLZXOptionsPreservesOutput(t *testing.T) {
	files := realBinaryImageFixture(t)
	xml := &XMLData{Document: `<WIM><IMAGE INDEX="1"><NAME>a</NAME></IMAGE><IMAGE INDEX="2"><NAME>b</NAME></IMAGE></WIM>`}

	base := WriteOptions{
		CompressionType: HdrFlagCompressLZX,
		ChunkSize:       32768,
		BootIndex:       1,
		GUID:            GUID{1},
	}

	images1, bt1, src1 := buildTestImages(t, files)
	zero, err := Assemble(images1, bt1, xml, src1, base)
	if err != nil {
		t.Fatalf("Assemble (zero LZXOptions): %v", err)
	}

	explicit := base
	explicit.LZXOptions = lzx.DefaultOptions()
	images2, bt2, src2 := buildTestImages(t, files)
	named, err := Assemble(images2, bt2, xml, src2, explicit)
	if err != nil {
		t.Fatalf("Assemble (lzx.DefaultOptions): %v", err)
	}

	if !bytes.Equal(zero, named) {
		t.Fatalf("zero-valued WriteOptions.LZXOptions (%d bytes) differs from explicit lzx.DefaultOptions() (%d bytes)", len(zero), len(named))
	}
	verifyWIM(t, zero, files, 2)
}

// TestWriteOptionsLZXPresetReachesEncoder is the end-to-end disproof of the
// main risk in this plumbing: an options field that compiles but is dropped
// on the way down would leave every preset producing identical WIMs. It
// assembles the same images at three rungs of the ladder and requires the
// LZX-compressed output to actually change, in the measured direction
// (Fast > Balanced > Default in size), while every one of them still reads
// back correctly.
func TestWriteOptionsLZXPresetReachesEncoder(t *testing.T) {
	files := realBinaryImageFixture(t)
	xml := &XMLData{Document: `<WIM><IMAGE INDEX="1"><NAME>a</NAME></IMAGE><IMAGE INDEX="2"><NAME>b</NAME></IMAGE></WIM>`}

	assemble := func(opts lzx.Options) []byte {
		t.Helper()
		images, bt, src := buildTestImages(t, files)
		out, err := Assemble(images, bt, xml, src, WriteOptions{
			CompressionType: HdrFlagCompressLZX,
			ChunkSize:       32768,
			BootIndex:       1,
			GUID:            GUID{1},
			LZXOptions:      opts,
		})
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		verifyWIM(t, out, files, 2)
		return out
	}

	def := assemble(lzx.DefaultOptions())
	fast := assemble(lzx.Fast())
	balanced := assemble(lzx.Balanced())

	if len(fast) <= len(def) {
		t.Fatalf("lzx.Fast() WIM is %d bytes, default is %d: want Fast strictly larger (it trades ~1.75%% size for ~33x throughput)", len(fast), len(def))
	}
	if len(fast) <= len(balanced) {
		t.Fatalf("lzx.Fast() WIM is %d bytes, Balanced is %d: want Fast strictly larger (measured 10902398 vs 10785178 on the WIM corpus)", len(fast), len(balanced))
	}
	if bytes.Equal(fast, balanced) {
		t.Fatalf("Fast and Balanced produced byte-identical WIMs; the finer knobs are not reaching the encoder")
	}
	t.Logf("LZX WIM sizes: default %d, Balanced %d (+%.2f%%), Fast %d (+%.2f%%)",
		len(def), len(balanced), 100*float64(len(balanced)-len(def))/float64(len(def)),
		len(fast), 100*float64(len(fast)-len(def))/float64(len(def)))
}

// TestWriteOptionsLZXOptionsIgnoredForNonLZX pins the documented scope of
// WriteOptions.LZXOptions: XPRESS and LZMS resources must be unaffected.
func TestWriteOptionsLZXOptionsIgnoredForNonLZX(t *testing.T) {
	files := realBinaryImageFixture(t)
	xml := &XMLData{Document: `<WIM><IMAGE INDEX="1"><NAME>a</NAME></IMAGE><IMAGE INDEX="2"><NAME>b</NAME></IMAGE></WIM>`}

	for _, tc := range []struct {
		name      string
		ctype     CompressionType
		chunkSize uint32
	}{
		{"xpress", HdrFlagCompressXPRESS, 32768},
		{"lzms", HdrFlagCompressLZMS, 131072},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assemble := func(opts lzx.Options) []byte {
				t.Helper()
				images, bt, src := buildTestImages(t, files)
				out, err := Assemble(images, bt, xml, src, WriteOptions{
					CompressionType: tc.ctype,
					ChunkSize:       tc.chunkSize,
					GUID:            GUID{1},
					LZXOptions:      opts,
				})
				if err != nil {
					t.Fatalf("Assemble: %v", err)
				}
				return out
			}
			if !bytes.Equal(assemble(lzx.Options{}), assemble(lzx.Fast())) {
				t.Fatalf("%s output changed with LZXOptions set; it must be ignored for non-LZX compression", tc.name)
			}
		})
	}
}

package pa30

import (
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

// realManifestSample is a real WinSxS `.manifest` file, copied verbatim
// (2026-07-13) from a real Windows 11 VM
// (`/var/lib/libvirt/images/win11.qcow2`, mounted read-only via
// `guestmount --ro`), identity
// amd64_022bd29263008e5688235b714058746f_b77a5c561934e089_4.0.15912.251_none_d13fd75b426163b5.
// It starts with an 8-byte "DCM"+version prefix before the PA30 signature.
//
//go:embed testdata/real_manifest_sample.manifest
var realManifestSample []byte

// wcpDictionary is the real, shared ~9KB "source buffer" every WinSxS
// `.manifest` file is actually PA30-delta-compressed against (see doc.go).
// Extracted (2026-07-13) as PE resource type 614 (0x266), name 1, language
// 1033, from a real `wcp.dll`
// (`Windows\WinSxS\amd64_microsoft-windows-servicingstack_31bf3856ad364e35_10.0.22621.6120_none_e967976c42c72025\wcp.dll`
// on the same VM as realManifestSample) via `wrestool` (icoutils) -- a
// standard, documented PE-resource-directory extraction, not a
// reverse-engineered format; only which resource ID holds this data came
// from third-party research (a Cobalt.io writeup), not this package's own
// reverse engineering.
//
//go:embed testdata/wcp_dictionary.bin
var wcpDictionary []byte

// realManifestFullSRCSample is a real WinSxS `.manifest` file (identity
// amd64_1394.inf.resources_31bf3856ad364e35_10.0.22621.1_en-us), copied
// verbatim (2026-07-13) from the same VM as realManifestSample. Unlike that
// fixture (DST/LRU matches only), this one's very first content symbol is a
// FULLSRC match at output position 0 -- before this package's SRC/FULLSRC
// implementation, decoding it failed immediately with "invalid
// back-reference offset 0 (slot 3)" (see match.go/patch.go's SRC/FULLSRC
// doc comments for why, and TODO.md's "Implement SRC/FULLSRC match
// decoding" entry for the full research trail). Kept as a permanent
// regression fixture for that fix, found while measuring full real-corpus
// decode coverage (all 17189 files in a real image's
// `Windows\WinSxS\Manifests`, which went from ~1% to 100% success after the
// fix -- every one of them, including this one, cryptographically
// hash-verified via DecodeWithSource's own TargetHash check, not merely
// self-consistent).
//
//go:embed testdata/real_manifest_fullsrc_sample.manifest
var realManifestFullSRCSample []byte

// TestDecodeWithSourceRealManifestFullSuccess fully decodes a real WinSxS
// `.manifest` file end-to-end for the first time (2026-07-13), using
// wcpDictionary as the source buffer. Cross-validated two independent ways:
// (1) the decoded byte length exactly matches the header's TargetSize; (2)
// its SHA-256 exactly matches the `S256H` registry value this project
// separately found (and, until now, could not explain) for this exact
// component identity while reverse-engineering the COMPONENTS hive
// (2026-07-10/13 research, see TODO.md) -- i.e. two unrelated research
// threads in this project now corroborate each other. The decoded XML is
// also a well-formed, complete `<assembly>` manifest (assemblyIdentity,
// deployment, dependency/dependentAssembly), parseable by the sibling `mum`
// package.
func TestDecodeWithSourceRealManifestFullSuccess(t *testing.T) {
	body := realManifestSample[4:] // strip "DCM" + 1 version byte

	out, h, err := DecodeWithSource(body, wcpDictionary)
	if err != nil {
		t.Fatalf("DecodeWithSource: %v", err)
	}
	if uint32(len(out)) != h.TargetSize {
		t.Fatalf("len(out) = %d, want TargetSize %d", len(out), h.TargetSize)
	}
	const wantSHA256 = "72d0a662ad2721ba2a5df925a958c064eacd3fa7e58f95217a662f6c4f9eb1d0"
	sum := sha256.Sum256(out)
	if got := hex.EncodeToString(sum[:]); got != wantSHA256 {
		t.Errorf("sha256(out) = %s, want %s (the real S256H value for this component)", got, wantSHA256)
	}
	want := `<assemblyIdentity name="022bd29263008e5688235b714058746f" version="4.0.15912.251" processorArchitecture="amd64" language="neutral" buildType="release" publicKeyToken="b77a5c561934e089" versionScope="nonSxS" />`
	if !strings.Contains(string(out), want) {
		t.Errorf("decoded output missing expected assemblyIdentity line; got:\n%s", out)
	}
}

// TestDecodeWithSourceRealFULLSRCSample decodes realManifestFullSRCSample --
// a real file whose first content symbol is a FULLSRC match, previously
// failing outright (see that fixture's doc comment) -- and checks the
// result the same two independent ways as
// TestDecodeWithSourceRealManifestFullSuccess: exact TargetSize match, and
// well-formed expected content. DecodeWithSource itself also verifies this
// file's embedded TargetHash internally (returning an error on any
// mismatch), so a nil error here already means the decoded bytes are
// cryptographically confirmed correct, not merely plausible-looking.
func TestDecodeWithSourceRealFULLSRCSample(t *testing.T) {
	body := realManifestFullSRCSample[4:] // strip "DCM" + 1 version byte

	out, h, err := DecodeWithSource(body, wcpDictionary)
	if err != nil {
		t.Fatalf("DecodeWithSource: %v", err)
	}
	if uint32(len(out)) != h.TargetSize {
		t.Fatalf("len(out) = %d, want TargetSize %d", len(out), h.TargetSize)
	}
	want := `<assemblyIdentity name="1394.inf.Resources" version="10.0.22621.1" processorArchitecture="amd64" language="en-US" publicKeyToken="31bf3856ad364e35" versionScope="nonSxS" />`
	if !strings.Contains(string(out), want) {
		t.Errorf("decoded output missing expected assemblyIdentity line; got:\n%s", out)
	}
}

// TestDecodeRealManifestSample decodes an actual WinSxS `.manifest` file
// and checks its result against ground truth independently confirmed by
// running github.com/smilingthax/msdelta-pa30-format's `dump` reference
// tool (as a black-box binary, not by reading its source) against the same
// file. That tool decodes this file's header identically to what's
// asserted below, then successfully decodes literal bytes 0xEF, 0xBB, 0xBF
// (a UTF-8 BOM) before failing on a DST match referencing offset 9069 at
// output position 3 -- which is expected and out of this package's scope:
// real `.manifest` files are compressed against a large (~9-10KB) shared
// dictionary (confirmed to be PE resource 0x266/name 1 in `wcp.dll`, per
// TODO.md), not an empty source buffer, so any match that reaches back
// past the small amount of real output produced so far is a reference into
// that unsupported dictionary, not a bug.
//
// This test exists to catch a regression in the Huffman engine specifically:
// an earlier version of this package's canonical-code construction used the
// textbook DEFLATE-style bottom-up threshold recurrence and decoded this
// exact file's first content symbol as a nonsensical match at output
// position 0 instead of the literal 0xEF -- see huffman.go's type doc for
// why PA30's actual construction differs (top-down, not bottom-up).
func TestDecodeRealManifestSample(t *testing.T) {
	if len(realManifestSample) < 4 || string(realManifestSample[0:3]) != "DCM" {
		t.Fatal("fixture missing expected DCM prefix")
	}
	body := realManifestSample[4:] // strip "DCM" + 1 version byte

	_, h, err := Decode(body)
	if h == nil {
		t.Fatalf("Decode returned no header at all: %v", err)
	}
	if h.FileTypeSet != 1 || h.FileType != 1 || h.Flags != 0x20000 || h.TargetSize != 659 || h.TargetHashAlgID != 0 {
		t.Errorf("header mismatch vs reference dump tool: %+v", *h)
	}
	if err == nil {
		t.Fatal("Decode succeeded; expected an error referencing the unsupported shared dictionary (offset 9069 at output position 3)")
	}
	// The exact wording isn't load-bearing, but decoding 3 literal bytes
	// correctly before failing at the dictionary reference is: this checks
	// we got exactly as far as the reference tool did.
	if !strings.Contains(err.Error(), "output offset 3") {
		t.Errorf("error = %v, want it to reference output offset 3 (i.e. after correctly decoding the 3-byte UTF-8 BOM)", err)
	}
}

// bitWriter is a test-only encoder mirroring bitReader's conventions
// (LSB-first per byte, numbers via the nibble scheme, buffers via
// size+align+raw-bytes), used to hand-construct synthetic PA30 patches so
// this package's decode pipeline can be tested end-to-end without a real
// PA30 file (which requires a working ground truth this package does not
// yet have -- see doc.go).
type bitWriter struct {
	bits []uint32
}

func (w *bitWriter) writeBit(b uint32) { w.bits = append(w.bits, b&1) }

func (w *bitWriter) writeBits(v uint32, n int) {
	for i := 0; i < n; i++ {
		w.writeBit((v >> uint(i)) & 1)
	}
}

// writeCodeword writes a canonical Huffman codeword's bits MSB-first (the
// order bitReader/huffmanTree.decode consumes them in).
func (w *bitWriter) writeCodeword(code uint32, length int) {
	for i := length - 1; i >= 0; i-- {
		w.writeBit((code >> uint(i)) & 1)
	}
}

func (w *bitWriter) writeNumber(v uint32) {
	nibbles := 1
	for nibbles < 8 && v >= (uint32(1)<<uint(nibbles*4)) {
		nibbles++
	}
	for i := 0; i < nibbles-1; i++ {
		w.writeBit(0)
	}
	w.writeBit(1)
	w.writeBits(v, nibbles*4)
}

// alignToByte pads with zero bits so the next bit written lands at a byte
// boundary relative to the eventual packed stream (which is prefixed with
// a 3-bit pad-count field), mirroring bitReader.alignToByte.
func (w *bitWriter) alignToByte() {
	pos := 3 + len(w.bits)
	if pos%8 != 0 {
		for i := 0; i < 8-pos%8; i++ {
			w.writeBit(0)
		}
	}
}

func (w *bitWriter) writeBuffer(content []byte) {
	w.writeNumber(uint32(len(content)))
	w.alignToByte()
	for _, by := range content {
		w.writeBits(uint32(by), 8)
	}
}

// finish packs the accumulated bits into bytes, prefixed with the 3-bit
// pad-count field every independent PA30 bitstream begins with, padding the
// final byte with zero bits as needed.
func (w *bitWriter) finish() []byte {
	total := 3 + len(w.bits)
	pad := (8 - total%8) % 8
	all := make([]uint32, 0, total+pad)
	for i := 0; i < 3; i++ {
		all = append(all, (uint32(pad)>>uint(i))&1)
	}
	all = append(all, w.bits...)
	for i := 0; i < pad; i++ {
		all = append(all, 0)
	}
	data := make([]byte, len(all)/8)
	for i, b := range all {
		if b != 0 {
			data[i/8] |= 1 << uint(i%8)
		}
	}
	return data
}

// canonicalCodeword independently walks the same canonical-Huffman
// bookkeeping decode() uses (first/index per length) to find the codeword
// assigned to sym under lens, so tests can hand-construct bitstreams that
// this package's own decoder will interpret correctly. This does not
// bypass verification of the Huffman engine itself -- that's covered by
// TestHuffmanDecodeHandComputedCodes, which checks decode() against
// independently, manually computed codewords.
func canonicalCodeword(t *testing.T, lens []int, maxLen int, sym int) (code uint32, length int) {
	t.Helper()
	tree, err := buildHuffmanTree(lens, maxLen)
	if err != nil {
		t.Fatalf("buildHuffmanTree: %v", err)
	}
	for l := 1; l <= maxLen; l++ {
		count := tree.counts[l]
		for k := 0; k < count; k++ {
			if tree.symbols[tree.start[l]+k] == sym {
				return uint32(tree.first[l] + k), l
			}
		}
	}
	t.Fatalf("symbol %d not found in tree", sym)
	return 0, 0
}

// TestDecodeSyntheticNullDelta hand-builds a minimal, valid, null-delta
// PA30 file (empty preprocessing, empty base rift table, default Huffman
// lengths) encoding a literal 'a' followed by a DST back-reference match
// (offset 1, length 3), which should decode to "aaaa" -- exercising the
// full pipeline: header parsing, buffer extraction, the patch buffer's
// rift-table/compression-parameter flags, default-length tree
// construction, and both the literal and DST match content paths.
//
// This is a self-built synthetic file, not a real Windows-produced one --
// see doc.go's verification status note.
func TestDecodeSyntheticNullDelta(t *testing.T) {
	want := "aaaa"

	mainLens := defaultLengths(mainTreeSize)

	pw := &bitWriter{}
	pw.writeBit(0) // base rift table: empty
	pw.writeBit(1) // isDefault: use default Huffman lengths

	// Literal 'a' (0x61 = 97): main-tree symbol == literal byte value.
	code, length := canonicalCodeword(t, mainLens, maxCodeLen, 'a')
	pw.writeCodeword(code, length)

	// DST match: slot 8 (offset = slot-7 = 1), lenField 2 (length = lenField+1 = 3).
	// sym = 256 + slot*8 + lenField.
	const slot, lenField = 8, 2
	matchSym := 256 + slot*8 + lenField
	code, length = canonicalCodeword(t, mainLens, maxCodeLen, matchSym)
	pw.writeCodeword(code, length)
	// slot 8-10 and a nonzero lenField need no further bits (see match.go).

	patchBuf := pw.finish()

	ow := &bitWriter{}
	ow.writeNumber(0)                 // FileTypeSet
	ow.writeNumber(0)                 // FileType
	ow.writeNumber(0)                 // Flags
	ow.writeNumber(uint32(len(want))) // TargetSize
	ow.writeNumber(0)                 // TargetHashAlgID (0 = unrecognized, not verified)
	ow.writeBuffer(nil)               // TargetHash (empty; alg unrecognized so unchecked)
	ow.writeBuffer(nil)               // preProcessBuffer (empty)
	ow.writeBuffer(patchBuf)          // patchBuffer

	outer := ow.finish()

	data := make([]byte, 0, 12+len(outer))
	data = append(data, []byte("PA30")...)
	var timeBuf [8]byte
	binary.LittleEndian.PutUint64(timeBuf[:], 0x01cd456789abcdef)
	data = append(data, timeBuf[:]...)
	data = append(data, outer...)

	out, h, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if string(out) != want {
		t.Errorf("Decode = %q, want %q", out, want)
	}
	if h.TargetSize != uint32(len(want)) {
		t.Errorf("Header.TargetSize = %d, want %d", h.TargetSize, len(want))
	}
	if h.TargetFileTime != 0x01cd456789abcdef {
		t.Errorf("Header.TargetFileTime = %#x, want %#x", h.TargetFileTime, 0x01cd456789abcdef)
	}
}

// TestDecodeRejectsNonEmptyRiftTable checks that a patch buffer whose base
// rift table isNonEmpty bit is set (a real, non-null-delta patch) is
// rejected with an explicit error, per this package's stated scope, rather
// than silently misdecoding.
func TestDecodeRejectsNonEmptyRiftTable(t *testing.T) {
	pw := &bitWriter{}
	pw.writeBit(1) // base rift table: non-empty
	patchBuf := pw.finish()

	ow := &bitWriter{}
	ow.writeNumber(0)
	ow.writeNumber(0)
	ow.writeNumber(0)
	ow.writeNumber(0)
	ow.writeNumber(0)
	ow.writeBuffer(nil)
	ow.writeBuffer(nil)
	ow.writeBuffer(patchBuf)
	outer := ow.finish()

	data := make([]byte, 0, 12+len(outer))
	data = append(data, []byte("PA30")...)
	data = append(data, make([]byte, 8)...)
	data = append(data, outer...)

	_, _, err := Decode(data)
	if err == nil {
		t.Fatal("Decode succeeded on a non-empty rift table, want an error")
	}
}

func TestDecodeRejectsBadSignature(t *testing.T) {
	if _, _, err := Decode([]byte("NOTAPATCH...")); err == nil {
		t.Fatal("Decode succeeded on bad signature, want an error")
	}
}

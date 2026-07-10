package regf

import (
	"bytes"
	"testing"
)

// buildMinimalHiveBytes hand-constructs a minimal but structurally valid
// regf hive: a base block, one hive bin, a root nk with two values (a
// REG_DWORD stored via the "data in offset" inline convention, and a
// REG_SZ stored in an out-of-line data cell) and one subkey ("Sub", an
// ASCII-named nk with no children of its own) reachable through an "lh"
// subkey list.
//
// It uses the package's cellArena (an internal byte-appending helper, not
// exercised by Parse) purely for offset bookkeeping, but hand-encodes every
// cell's field layout directly against the spec's byte offsets rather than
// reusing nkCell/appendToVK/etc., so that this fixture exercises Parse
// (and its callees) independently of the AppendTo/build code path in
// hive.go.
func buildMinimalHiveBytes(t *testing.T) []byte {
	t.Helper()

	// Reserve the hbin header's 32 bytes up front: cell offsets are
	// relative to the whole hive bins data area, which starts with that
	// header (see hive.go's AppendTo for the same convention).
	arena := &cellArena{buf: make([]byte, HBinHeaderSize)}

	// Child key "Sub" (ASCII name, no subkeys/values/security/class name).
	childName := []byte("Sub")
	child := make([]byte, nkHeaderSize+len(childName))
	copy(child[0:2], "nk")
	le.PutUint16(child[2:4], KeyFlagCompName)
	le.PutUint32(child[28:32], NoCellOffset) // subkeys offset
	le.PutUint32(child[32:36], NoCellOffset) // volatile subkeys offset
	le.PutUint32(child[40:44], NoCellOffset) // values offset
	le.PutUint32(child[44:48], NoCellOffset) // security offset
	le.PutUint32(child[48:52], NoCellOffset) // class name offset
	le.PutUint16(child[74:76], uint16(len(childName)))
	copy(child[nkHeaderSize:], childName)
	childOffset := arena.alloc(child)

	// Subkey list ("lh") with the one child, using the documented LH hash.
	hash := lhHash(childName, true)
	subkeyList := make([]byte, subkeyListHeaderSize+8)
	copy(subkeyList[0:2], "lh")
	le.PutUint16(subkeyList[2:4], 1)
	le.PutUint32(subkeyList[4:8], childOffset)
	le.PutUint32(subkeyList[8:12], hash)
	subkeyListOffset := arena.alloc(subkeyList)

	// Value 1: REG_DWORD 42, inline (data size 4, top bit set).
	dwordName := stringToUTF16LE("Count")
	dwordVK := make([]byte, vkHeaderSize+len(dwordName))
	copy(dwordVK[0:2], "vk")
	le.PutUint16(dwordVK[2:4], uint16(len(dwordName)))
	le.PutUint32(dwordVK[4:8], 4|dataSizeInlineFlag)
	le.PutUint32(dwordVK[8:12], 42) // inline data value
	le.PutUint32(dwordVK[12:16], RegDWORD)
	copy(dwordVK[vkHeaderSize:], dwordName)
	dwordVKOffset := arena.alloc(dwordVK)

	// Value 2: REG_SZ "Hello" (UTF-16LE, no terminator), out-of-line data
	// cell. ("Hello" is 10 bytes, unambiguously over maxInlineDataSize, so
	// AppendTo's own inline-vs-cell policy agrees with this fixture's
	// choice; see the comment on TestParseAppendToRoundTrip.)
	szData := stringToUTF16LE("Hello")
	szDataOffset := arena.alloc(szData)
	szName := stringToUTF16LE("Name")
	szVK := make([]byte, vkHeaderSize+len(szName))
	copy(szVK[0:2], "vk")
	le.PutUint16(szVK[2:4], uint16(len(szName)))
	le.PutUint32(szVK[4:8], uint32(len(szData)))
	le.PutUint32(szVK[8:12], szDataOffset)
	le.PutUint32(szVK[12:16], RegSZ)
	copy(szVK[vkHeaderSize:], szName)
	szVKOffset := arena.alloc(szVK)

	// Values list: flat array of vk offsets, no header (version 1.2+).
	valuesList := make([]byte, 8)
	le.PutUint32(valuesList[0:4], dwordVKOffset)
	le.PutUint32(valuesList[4:8], szVKOffset)
	valuesListOffset := arena.alloc(valuesList)

	// Root nk: unnamed, KEY_HIVE_ENTRY, 1 subkey, 2 values.
	root := make([]byte, nkHeaderSize)
	copy(root[0:2], "nk")
	le.PutUint16(root[2:4], KeyFlagHiveEntry)
	le.PutUint32(root[16:20], NoCellOffset) // parent key offset: root has none
	le.PutUint32(root[20:24], 1)            // number of sub keys
	le.PutUint32(root[28:32], subkeyListOffset)
	le.PutUint32(root[32:36], NoCellOffset) // volatile subkeys offset
	le.PutUint32(root[36:40], 2)            // number of values
	le.PutUint32(root[40:44], valuesListOffset)
	le.PutUint32(root[44:48], NoCellOffset)           // security offset
	le.PutUint32(root[48:52], NoCellOffset)           // class name offset
	le.PutUint32(root[52:56], uint32(len(childName))) // largest subkey name size
	le.PutUint32(root[60:64], uint32(len(dwordName))) // largest value name size ("Count" > "Name")
	le.PutUint32(root[64:68], uint32(len(szData)))    // largest value data size ("Hello" > 4-byte dword)
	rootOffset := arena.alloc(root)

	// Patch the child's parent-key offset now that the root's own offset is
	// known, matching what Hive.AppendTo's builder does (see buildKeyCell
	// in hive.go): this field is otherwise unused by Parse (it does not
	// validate it), but keeping it correct here means Parse . AppendTo is
	// an exact round trip rather than merely an equivalent tree.
	patchUint32(arena.buf, childOffset, 16, rootOffset)

	// Fill the remainder of a single 4096-byte hive bin with one free cell.
	need := len(arena.buf)
	binSize := ((need + (BaseBlockSize - 1)) / BaseBlockSize) * BaseBlockSize
	if binSize < BaseBlockSize {
		binSize = BaseBlockSize
	}
	if free := binSize - need; free > 0 {
		filler := make([]byte, free)
		le.PutUint32(filler[0:4], uint32(free))
		arena.buf = append(arena.buf, filler...)
	}
	bin := &HBin{Size: uint32(binSize)}
	bin.writeHeader(arena.buf)

	bb := &BaseBlock{
		PrimarySequence:   1,
		SecondarySequence: 1,
		MajorVersion:      1,
		MinorVersion:      Version1_5,
		FileType:          FileTypePrimary,
		FileFormat:        1,
		RootCellOffset:    rootOffset,
		HiveBinsDataSize:  uint32(binSize),
		ClusteringFactor:  1,
	}
	out, err := bb.AppendTo(nil)
	if err != nil {
		t.Fatalf("BaseBlock.AppendTo: %v", err)
	}
	out = append(out, arena.buf...)
	return out
}

func TestParseMinimalHive(t *testing.T) {
	data := buildMinimalHiveBytes(t)

	hive, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	root := hive.Root
	if root == nil {
		t.Fatal("Parse returned nil root")
	}
	if !root.IsRoot() {
		t.Error("root key missing KeyFlagHiveEntry")
	}
	if root.NameUTF8() != "" {
		t.Errorf("root name = %q, want empty", root.NameUTF8())
	}

	if len(root.Values) != 2 {
		t.Fatalf("root has %d values, want 2", len(root.Values))
	}
	dword, sz := root.Values[0], root.Values[1]
	if dword.NameUTF8() != "Count" || dword.Type != RegDWORD || !bytes.Equal(dword.Data, []byte{42, 0, 0, 0}) {
		t.Errorf("dword value = %+v", dword)
	}
	if sz.NameUTF8() != "Name" || sz.Type != RegSZ || utf16leToString(sz.Data) != "Hello" {
		t.Errorf("sz value = %+v", sz)
	}

	if len(root.Subkeys) != 1 {
		t.Fatalf("root has %d subkeys, want 1", len(root.Subkeys))
	}
	sub := root.Subkeys[0]
	if sub.NameUTF8() != "Sub" {
		t.Errorf("subkey name = %q, want %q", sub.NameUTF8(), "Sub")
	}
	if sub.Flags&KeyFlagCompName == 0 {
		t.Error("subkey missing KeyFlagCompName")
	}
}

// TestParseAppendToRoundTrip checks that Parse followed by AppendTo
// reproduces the hand-built fixture's bytes exactly. This holds because the
// fixture above was deliberately built in the same cell order (children,
// then subkey list, then values, then own nk) and single-hbin/no-padding
// layout that Hive.AppendTo's allocation strategy itself produces (see the
// doc comment on AppendTo in hive.go).
func TestParseAppendToRoundTrip(t *testing.T) {
	data := buildMinimalHiveBytes(t)

	hive, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	out, err := hive.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Fatalf("round trip mismatch:\norig = % x\n out = % x", data, out)
	}
}

// TestBuildFromStructLiterals builds a Hive purely from Go struct literals
// (no Parse involved) and checks that AppendTo's output parses back with
// Parse into an equivalent tree -- exercising the "build a fresh hive from
// scratch" path a caller (not a Parse round trip) would use.
func TestBuildFromStructLiterals(t *testing.T) {
	bigData := bytes.Repeat([]byte("0123456789abcdef"), 1200) // 19200 bytes, forces "db" big data

	hive := &Hive{
		BaseBlock: BaseBlock{
			MajorVersion:     1,
			MinorVersion:     Version1_5,
			FileType:         FileTypePrimary,
			ClusteringFactor: 1,
		},
		Root: &Key{
			Flags: KeyFlagHiveEntry,
			Values: []Value{
				{Name: stringToUTF16LE("Count"), Type: RegDWORD, Data: []byte{42, 0, 0, 0}},
				{Name: stringToUTF16LE("Name"), Type: RegSZ, Data: stringToUTF16LE("Hello, world")},
				{Name: stringToUTF16LE("Big"), Type: RegBinary, Data: bigData},
				{Name: nil, Type: RegSZ, Data: stringToUTF16LE("default value")},
			},
			Security: []byte{0x01, 0x00, 0x04, 0x80}, // opaque, not a real SD
			Subkeys: []*Key{
				{
					Name:  []byte("Child1"),
					Flags: KeyFlagCompName,
				},
				{
					Name:      []byte("Child2"),
					Flags:     KeyFlagCompName,
					ClassName: stringToUTF16LE("SomeClass"),
					Values: []Value{
						{Name: stringToUTF16LE("X"), Type: RegQWORD, Data: []byte{1, 2, 3, 4, 5, 6, 7, 8}},
					},
				},
			},
		},
	}

	data, err := hive.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}

	got, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse of built hive: %v", err)
	}

	root := got.Root
	if len(root.Values) != 4 {
		t.Fatalf("root has %d values, want 4", len(root.Values))
	}
	if root.Values[0].NameUTF8() != "Count" || !bytes.Equal(root.Values[0].Data, []byte{42, 0, 0, 0}) {
		t.Errorf("Count value = %+v", root.Values[0])
	}
	if root.Values[1].NameUTF8() != "Name" || utf16leToString(root.Values[1].Data) != "Hello, world" {
		t.Errorf("Name value = %+v", root.Values[1])
	}
	if !bytes.Equal(root.Values[2].Data, bigData) {
		t.Errorf("Big value data mismatch: got %d bytes, want %d", len(root.Values[2].Data), len(bigData))
	}
	if root.Values[3].NameUTF8() != "" || utf16leToString(root.Values[3].Data) != "default value" {
		t.Errorf("default value = %+v", root.Values[3])
	}
	if !bytes.Equal(root.Security, hive.Root.Security) {
		t.Errorf("Security = % x, want % x", root.Security, hive.Root.Security)
	}

	if len(root.Subkeys) != 2 {
		t.Fatalf("root has %d subkeys, want 2", len(root.Subkeys))
	}
	names := map[string]*Key{}
	for _, k := range root.Subkeys {
		names[k.NameUTF8()] = k
	}
	if _, ok := names["Child1"]; !ok {
		t.Error("missing Child1")
	}
	c2, ok := names["Child2"]
	if !ok {
		t.Fatal("missing Child2")
	}
	if utf16leToString(c2.ClassName) != "SomeClass" {
		t.Errorf("Child2 class name = %q", utf16leToString(c2.ClassName))
	}
	if len(c2.Values) != 1 || c2.Values[0].NameUTF8() != "X" || c2.Values[0].Type != RegQWORD {
		t.Errorf("Child2 values = %+v", c2.Values)
	}

	// AppendTo'd output from this package's own build should itself be a
	// tight round trip (Parse . AppendTo == identity), since the fixture is
	// simple/deterministic by construction (see hive.go's AppendTo doc).
	reserialized, err := got.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo (2nd generation): %v", err)
	}
	if !bytes.Equal(reserialized, data) {
		t.Fatalf("built-hive round trip mismatch: lengths %d vs %d", len(reserialized), len(data))
	}
}

// TestBaseBlockRoundTrip checks BaseBlock.AppendTo/ParseBaseBlock in
// isolation, including checksum computation and rejection of a corrupted
// checksum.
func TestBaseBlockRoundTrip(t *testing.T) {
	bb := &BaseBlock{
		PrimarySequence:   7,
		SecondarySequence: 7,
		LastWritten:       0x01d8a1b2c3d4e5f6,
		MajorVersion:      1,
		MinorVersion:      Version1_5,
		FileType:          FileTypePrimary,
		FileFormat:        1,
		RootCellOffset:    0x20,
		HiveBinsDataSize:  0x1000,
		ClusteringFactor:  1,
	}
	copy(bb.FileName[:], stringToUTF16LE("SYSTEM"))

	data, err := bb.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}
	if len(data) != BaseBlockSize {
		t.Fatalf("len(data) = %d, want %d", len(data), BaseBlockSize)
	}

	// AppendTo recomputes the checksum; set the expectation to match before
	// comparing (bb.Checksum was left zero above).
	bb.Checksum, err = ComputeChecksum(data)
	if err != nil {
		t.Fatalf("ComputeChecksum: %v", err)
	}

	got, err := ParseBaseBlock(data)
	if err != nil {
		t.Fatalf("ParseBaseBlock: %v", err)
	}
	if *got != *bb {
		t.Errorf("round trip mismatch:\n got  = %+v\n want = %+v", *got, *bb)
	}

	corrupt := append([]byte(nil), data...)
	corrupt[0] ^= 0xff
	if _, err := ParseBaseBlock(corrupt); err == nil {
		t.Error("ParseBaseBlock accepted bad magic")
	}

	corrupt = append([]byte(nil), data...)
	corrupt[100] ^= 0xff // inside the checksummed range, not the magic
	if _, err := ParseBaseBlock(corrupt); err == nil {
		t.Error("ParseBaseBlock accepted a corrupted checksum")
	}
}

// TestLHHash checks the documented LH hash algorithm against a hand
// computed example ("AB" uppercased is itself; hash = ((0*37)+'A')*37+'B').
func TestLHHash(t *testing.T) {
	want := uint32('A')*37 + uint32('B')
	if got := lhHash([]byte("ab"), true); got != want {
		t.Errorf("lhHash(%q) = %d, want %d", "ab", got, want)
	}
}

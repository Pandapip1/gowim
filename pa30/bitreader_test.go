package pa30

import "testing"

// TestReadNumberWorkedExample reproduces the exact worked bit-stream example
// from github.com/smilingthax/msdelta-pa30-format's README.md ("BitStreams"
// section): a 3-bit pad prefix of value 1, followed by number 14 encoded as
// a 1-nibble field ("1" then "0111"), followed by number 17 encoded as a
// 2-nibble field ("01" then "10001000"), which the README states hex-dumps
// to "e9 46 ...". This is real ground truth from the format's own spec
// author, not a self-consistency check against this package's own code.
func TestReadNumberWorkedExample(t *testing.T) {
	// Byte 0 (bits 0-7):  1,0,0, 1, 0,1,1,1 -> 0xE9 (per README)
	// Byte 1 (bits 8-15): 0,1,   1,0,0,0,1,0 -> 0x46 (per README)
	// Byte 2 (bits 16-17 carry the value-17 field's last 2 bits, 0,0; the
	// rest of byte 2 is unspecified by the README's example, set to 0).
	data := []byte{0xE9, 0x46, 0x00}

	br, err := newBitReader(data)
	if err != nil {
		t.Fatalf("newBitReader: %v", err)
	}
	if br.pad != 1 {
		t.Fatalf("pad = %d, want 1 (per README's worked example)", br.pad)
	}

	v1, err := br.readNumber()
	if err != nil {
		t.Fatalf("readNumber (first): %v", err)
	}
	if v1 != 14 {
		t.Errorf("first number = %d, want 14", v1)
	}

	v2, err := br.readNumber()
	if err != nil {
		t.Fatalf("readNumber (second): %v", err)
	}
	if v2 != 17 {
		t.Errorf("second number = %d, want 17", v2)
	}
}

// TestReadBitsOrder checks the basic LSB-first, non-byte-swapped bit
// reading contract a single byte 0b00000101 (0x05) yields bits 1,0,1,0,0,0,0,0
// in read order.
func TestReadBitsOrder(t *testing.T) {
	// First 3 bits are consumed as the pad prefix by newBitReader; use a
	// byte whose low 3 bits are 0 (pad=0) so the remaining 5 bits are easy
	// to check by hand: 0x28 = 0b00101000 -> bits (LSB first): 0,0,0,1,0,1,0,0
	// pad = bits[0:3] = 0,0,0 = 0. Remaining bits[3:8] = 1,0,1,0,0.
	data := []byte{0x28}
	br, err := newBitReader(data)
	if err != nil {
		t.Fatalf("newBitReader: %v", err)
	}
	if br.pad != 0 {
		t.Fatalf("pad = %d, want 0", br.pad)
	}
	want := []uint32{1, 0, 1, 0, 0}
	for i, w := range want {
		got, err := br.readBit()
		if err != nil {
			t.Fatalf("readBit %d: %v", i, err)
		}
		if got != w {
			t.Errorf("bit %d = %d, want %d", i, got, w)
		}
	}
}

func TestAlignToByteAndRawBytes(t *testing.T) {
	// pad prefix (3 bits) = 0, then 5 more bits (any), then aligned raw bytes.
	data := []byte{0x00, 0xAA, 0xBB}
	br, err := newBitReader(data)
	if err != nil {
		t.Fatalf("newBitReader: %v", err)
	}
	if _, err := br.readBits(5); err != nil {
		t.Fatalf("readBits: %v", err)
	}
	br.alignToByte()
	raw, err := br.readRawBytes(2)
	if err != nil {
		t.Fatalf("readRawBytes: %v", err)
	}
	if raw[0] != 0xAA || raw[1] != 0xBB {
		t.Errorf("raw = % x, want aa bb", raw)
	}
}

package iso

import (
	"bytes"
	"testing"
	"time"
)

// TestBothByteOrders checks the two "both-byte orders" encodings against the
// worked examples the standard itself gives, which is the only way to be
// sure the odd doubled layout is right rather than merely plausible.
func TestBothByteOrders(t *testing.T) {
	// ECMA-119 7.2.3 NOTE: "the decimal number 4660 has (12 34) as its
	// hexadecimal representation and is recorded as (34 12 12 34)."
	var b16 [4]byte
	put723(b16[:], 4660)
	if want := []byte{0x34, 0x12, 0x12, 0x34}; !bytes.Equal(b16[:], want) {
		t.Errorf("put723(4660) = % x, want % x", b16, want)
	}

	// ECMA-119 7.3.3 NOTE: "the decimal number 305419896 has (12 34 56 78)
	// as its hexadecimal representation and is recorded as
	// (78 56 34 12 12 34 56 78)."
	var b32 [8]byte
	put733(b32[:], 305419896)
	if want := []byte{0x78, 0x56, 0x34, 0x12, 0x12, 0x34, 0x56, 0x78}; !bytes.Equal(b32[:], want) {
		t.Errorf("put733(305419896) = % x, want % x", b32, want)
	}

	// ECMA-119 7.2.1/7.2.2 and 7.3.1/7.3.2 NOTEs, same two numbers.
	var l16, m16 [2]byte
	put721(l16[:], 4660)
	put722(m16[:], 4660)
	if l16 != [2]byte{0x34, 0x12} || m16 != [2]byte{0x12, 0x34} {
		t.Errorf("put721/put722(4660) = % x / % x", l16, m16)
	}
	var l32, m32 [4]byte
	put731(l32[:], 305419896)
	put732(m32[:], 305419896)
	if l32 != [4]byte{0x78, 0x56, 0x34, 0x12} || m32 != [4]byte{0x12, 0x34, 0x56, 0x78} {
		t.Errorf("put731/put732(305419896) = % x / % x", l32, m32)
	}
}

func TestLongDateTime(t *testing.T) {
	// ECMA-119 8.4.26.1 Table 5. 2026-08-19T11:22:33.44 at UTC+02:00,
	// which is +8 quarter-hour intervals.
	tm := time.Date(2026, 8, 19, 11, 22, 33, 440000000, time.FixedZone("x", 2*3600))
	var b [17]byte
	putLongDateTime(b[:], tm)
	if got, want := string(b[:16]), "2026081911223344"; got != want {
		t.Errorf("digits = %q, want %q", got, want)
	}
	if int8(b[16]) != 8 {
		t.Errorf("GMT offset = %d, want 8", int8(b[16]))
	}

	// 8.4.26.1: "If all characters in RBP 1 to 16 of this field are the
	// digit ZERO and the number in RBP 17 is zero, it shall mean that the
	// date and time are not specified."
	putLongDateTime(b[:], time.Time{})
	if got, want := string(b[:]), "0000000000000000\x00"; got != want {
		t.Errorf("unspecified = %q, want %q", got, want)
	}
}

func TestShortDateTime(t *testing.T) {
	// ECMA-119 9.1.5 Table 9: seven 8-bit numbers, the first being the
	// number of years since 1900.
	tm := time.Date(2026, 8, 19, 11, 22, 33, 0, time.FixedZone("x", -5*3600))
	var b [7]byte
	putShortDateTime(b[:], tm)
	want := [7]byte{126, 8, 19, 11, 22, 33, 0xEC} // 0xEC = -20 as a two's complement 8-bit number (7.1.2)
	if b != want {
		t.Errorf("putShortDateTime = % d, want % d", b, want)
	}

	// 9.1.5: "If all seven numbers are zero, it shall mean that the date
	// and time are not specified."
	putShortDateTime(b[:], time.Time{})
	if b != [7]byte{} {
		t.Errorf("unspecified = % d, want all zero", b)
	}
}

func TestCharacterSets(t *testing.T) {
	// ECMA-119 7.4.1 says there are exactly 37 d-characters and exactly 57
	// a-characters. Counting them is a cheap, decisive check that the
	// column/row ranges were transcribed correctly.
	nd, na := 0, 0
	for c := 0; c < 256; c++ {
		if isDChar(byte(c)) {
			nd++
		}
		if isAChar(byte(c)) {
			na++
		}
	}
	if nd != 37 {
		t.Errorf("d-characters = %d, want 37", nd)
	}
	if na != 57 {
		t.Errorf("a-characters = %d, want 57", na)
	}
	// Spot-check the exclusions that are easy to get wrong.
	for _, c := range []byte{'#', '$', '@', '[', '\\', ']', '^', 'a', '`', 0x7F} {
		if isAChar(c) {
			t.Errorf("isAChar(%q) = true, want false", c)
		}
	}
	for _, c := range []byte{'.', ';', '-', ' ', 'a'} {
		if isDChar(c) {
			t.Errorf("isDChar(%q) = true, want false", c)
		}
	}
}

func TestSectionLengths(t *testing.T) {
	// ECMA-119 6.4.5 permits a zero-length File Section, and a file still
	// needs a Directory Record, so an empty file is one empty section.
	if got := sectionLengths(0, 2048); len(got) != 1 || got[0] != 0 {
		t.Errorf("sectionLengths(0) = %v, want [0]", got)
	}
	if got := sectionLengths(2048, 2048); len(got) != 1 || got[0] != 2048 {
		t.Errorf("sectionLengths(2048, 2048) = %v, want [2048]", got)
	}
	if got := sectionLengths(2049, 2048); len(got) != 2 || got[0] != 2048 || got[1] != 1 {
		t.Errorf("sectionLengths(2049, 2048) = %v, want [2048 1]", got)
	}
	// Every section but the last must be exactly max so that each ends on
	// an Extent boundary.
	got := sectionLengths(5000, 2048)
	if len(got) != 3 || got[0] != 2048 || got[1] != 2048 || got[2] != 904 {
		t.Errorf("sectionLengths(5000, 2048) = %v", got)
	}
}

func TestMangling(t *testing.T) {
	tests := []struct {
		name  string
		level InterchangeLevel
		want  string
	}{
		// ECMA-119 10.1: 8 d-characters for the File Name, 3 for the
		// File Name Extension.
		{"README.TXT", Level1, "README.TXT"},
		{"verylongfilename.text", Level1, "VERYLONG.TEX"},
		{"lower.txt", Level1, "LOWER.TXT"},
		{"has-dash.txt", Level1, "HAS_DASH.TXT"},
		// Level 2/3 allow name+extension up to 30 (7.5.1).
		{"verylongfilename.text", Level2, "VERYLONGFILENAME.TEXT"},
		{"noext", Level2, "NOEXT."},
		{".bashrc", Level2, "_BASHRC."},
	}
	for _, tc := range tests {
		if got := mangleFileName(tc.name, tc.level); got != tc.want {
			t.Errorf("mangleFileName(%q, %d) = %q, want %q", tc.name, tc.level, got, tc.want)
		}
	}
	if got := mangleDirName("some.dir-name", Level1); got != "SOME_DIR" {
		t.Errorf("mangleDirName = %q", got)
	}
}

// TestOrdering checks the ECMA-119 9.3 ordering, in particular the FILLER
// padding rule, which is what makes a shorter name sort before a longer one
// that shares its prefix (FILLER is (20), below every d-character).
func TestOrdering(t *testing.T) {
	if compareFileIdentifiers("AB", "TXT", 1, "ABC", "TXT", 1) >= 0 {
		t.Error("AB should sort before ABC")
	}
	if compareFileIdentifiers("A", "TXT", 1, "A", "BIN", 1) <= 0 {
		t.Error("A.BIN should sort before A.TXT")
	}
	// 9.3 orders version numbers in *descending* order.
	if compareFileIdentifiers("A", "TXT", 2, "A", "TXT", 1) >= 0 {
		t.Error("higher version should sort first")
	}
}

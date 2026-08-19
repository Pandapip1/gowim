package iso

import (
	"time"
)

// LogicalSectorSize is the size in bytes of a Logical Sector.
//
// ECMA-119 6.1.2 defines a Logical Sector as 2^n bytes, not less than 2048.
// Every real CD-ROM/DVD ISO uses exactly 2048, and this package's Logical
// Block size (6.2.2, which "shall not be greater than the Logical Sector
// size") is set equal to it. genisoimage hardcodes the same value as
// SECTOR_SIZE.
const LogicalSectorSize = 2048

// systemAreaSectors is the size of the System Area in Logical Sectors.
//
// ECMA-119 6.2.1: "The System Area shall occupy the Logical Sectors with
// Logical Sector Numbers 0 to 15." Its content is explicitly not specified
// by the standard, which is why an x86 MBR or a hybrid boot sector can live
// there. This package zero-fills it; a later El Torito/hybrid phase may
// want to stamp bytes into it.
const systemAreaSectors = 16

// firstVolumeDescriptorSector is where the Volume Descriptor Set begins.
//
// ECMA-119 6.3: "The Volume Descriptors shall be recorded in consecutively
// numbered Logical Sectors starting with the Logical Sector having Logical
// Sector Number 16."
const firstVolumeDescriptorSector = systemAreaSectors

// filler is the FILLER bit combination, used to pad character fields on the
// right and, per 9.3 and 6.9.1, notionally used when comparing identifiers
// of unequal length.
//
// ECMA-119 7.4.3.2: "Within a volume that is identified by a Primary Volume
// Descriptor or by a Supplementary Volume Descriptor, the bit combination
// of FILLER shall be (20)." That is an ASCII space.
const filler = byte(0x20)

// put711 records an 8-bit unsigned numerical value (ECMA-119 7.1.1).
func put711(b []byte, v uint8) {
	b[0] = v
}

// put712 records an 8-bit signed numerical value as a two's complement
// number (ECMA-119 7.1.2). Used only for the GMT offset field of the two
// date-and-time formats (8.4.26.1 RBP 17, 9.1.5 RBP 7).
func put712(b []byte, v int8) {
	b[0] = byte(v)
}

// put721 records a 16-bit value least significant byte first (ECMA-119
// 7.2.1), the encoding used by a Type L Path Table (6.9.2).
func put721(b []byte, v uint16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}

// put722 records a 16-bit value most significant byte first (ECMA-119
// 7.2.2), the encoding used by a Type M Path Table (6.9.2).
func put722(b []byte, v uint16) {
	b[0] = byte(v >> 8)
	b[1] = byte(v)
}

// put723 records a 16-bit value in both-byte orders (ECMA-119 7.2.3): the
// value appears LSB-first in the first two bytes and MSB-first in the next
// two, i.e. (wx yz) is recorded as (yz wx wx yz).
func put723(b []byte, v uint16) {
	put721(b[0:2], v)
	put722(b[2:4], v)
}

// put731 records a 32-bit value least significant byte first (ECMA-119
// 7.3.1).
func put731(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

// put732 records a 32-bit value most significant byte first (ECMA-119
// 7.3.2).
func put732(b []byte, v uint32) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}

// put733 records a 32-bit value in both-byte orders (ECMA-119 7.3.3):
// (st uv wx yz) is recorded in an eight-byte field as
// (yz wx uv st st uv wx yz).
//
// This redundant encoding is why a Directory Record's Location of Extent
// and Data Length fields are eight bytes wide for a 32-bit quantity (9.1.3,
// 9.1.4), and it is the reason a single File Section cannot exceed
// 4 GiB - 1: the field is 32 bits no matter how it is spelled.
func put733(b []byte, v uint32) {
	put731(b[0:4], v)
	put732(b[4:8], v)
}

// putStrPad copies s into b, truncating if too long and padding the
// remainder on the right with FILLER.
//
// ECMA-119 7.4.5 ("Justification of characters") requires character fields
// to be left-justified and padded on the right with FILLER. Callers are
// responsible for having already reduced s to legal a-characters or
// d-characters (7.4.1); see name.go.
func putStrPad(b []byte, s string) {
	n := copy(b, s)
	for i := n; i < len(b); i++ {
		b[i] = filler
	}
}

// putStrZeroPad copies s into b, truncating it if necessary, and pads the
// remainder with (00) rather than with FILLER.
//
// This is for the El Torito fields, which are not ECMA-119 character fields
// and are specified with zero padding: the Boot System Identifier of the Boot
// Record Volume Descriptor "must be 'EL TORITO SPECIFICATION' padded with
// 0's" (El Torito 1.0 Figure 7), and the Validation Entry's ID string is
// likewise zero-filled by every producer measured.
func putStrZeroPad(b []byte, s string) {
	n := copy(b, s)
	putZero(b[n:])
}

// putZero fills b with (00) bytes, for the fields ECMA-119 describes as
// "Unused Field" or "Reserved for future standardization" (e.g. 8.4.4,
// 8.4.7, 8.4.9, 8.4.31, 8.4.33), all of which are specified to be (00).
func putZero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// gmtOffsetQuarters converts a time zone offset in seconds east of UTC into
// the ECMA-119 offset unit: "Offset from Greenwich Mean Time in number of
// 15 min intervals from -48 (West) to +52 (East)" (8.4.26.1 RBP 17 and
// 9.1.5 RBP 7).
//
// Offsets outside the representable range, and offsets that are not a whole
// number of quarter hours, are clamped/truncated rather than rejected: no
// real time zone violates either bound, and failing to write an image over
// a clock setting would be a poor trade.
func gmtOffsetQuarters(t time.Time) int8 {
	_, offsetSeconds := t.Zone()
	q := offsetSeconds / (15 * 60)
	if q < -48 {
		q = -48
	}
	if q > 52 {
		q = 52
	}
	return int8(q)
}

// putLongDateTime records the 17-byte "Date and Time Format" of ECMA-119
// 8.4.26.1, used by the four volume date fields of the Primary Volume
// Descriptor (8.4.26 to 8.4.29).
//
// Layout (8.4.26.1 Table 5), all digit fields being ASCII decimal:
//
//	RBP 1..4   Year from 1 to 9999
//	RBP 5..6   Month of the year from 1 to 12
//	RBP 7..8   Day of the month from 1 to 31
//	RBP 9..10  Hour of the day from 0 to 23
//	RBP 11..12 Minute of the hour from 0 to 59
//	RBP 13..14 Second of the minute from 0 to 59
//	RBP 15..16 Hundredths of a second
//	RBP 17     GMT offset in 15-minute intervals, per 7.1.2
//
// A zero time is recorded as the "not specified" form the clause defines:
// all sixteen digit positions set to the digit ZERO and RBP 17 set to zero.
func putLongDateTime(b []byte, t time.Time) {
	if t.IsZero() {
		for i := 0; i < 16; i++ {
			b[i] = '0'
		}
		b[16] = 0
		return
	}
	putDigits(b[0:4], t.Year())
	putDigits(b[4:6], int(t.Month()))
	putDigits(b[6:8], t.Day())
	putDigits(b[8:10], t.Hour())
	putDigits(b[10:12], t.Minute())
	putDigits(b[12:14], t.Second())
	putDigits(b[14:16], t.Nanosecond()/10000000)
	put712(b[16:17], gmtOffsetQuarters(t))
}

// putShortDateTime records the 7-byte "Recording Date and Time" of
// ECMA-119 9.1.5, used by Directory Records. Unlike the volume-level
// format above it is seven 8-bit *numbers*, not ASCII digits:
//
//	RBP 1 Number of years since 1900
//	RBP 2 Month of the year from 1 to 12
//	RBP 3 Day of the month from 1 to 31
//	RBP 4 Hour of the day from 0 to 23
//	RBP 5 Minute of the hour from 0 to 59
//	RBP 6 Second of the minute from 0 to 59
//	RBP 7 GMT offset in 15-minute intervals, per 7.1.2
//
// 9.1.5: "If all seven numbers are zero, it shall mean that the date and
// time are not specified", which is how a zero time is recorded here.
//
// Note the year field is a single byte holding (year - 1900), so this
// format cannot represent a date before 1900 or after 2155. Times outside
// that window are clamped to the endpoints rather than wrapping, because a
// wrapped year silently produces a plausible-looking wrong date.
func putShortDateTime(b []byte, t time.Time) {
	if t.IsZero() {
		putZero(b[0:7])
		return
	}
	year := t.Year() - 1900
	if year < 0 {
		year = 0
	}
	if year > 255 {
		year = 255
	}
	put711(b[0:1], uint8(year))
	put711(b[1:2], uint8(t.Month()))
	put711(b[2:3], uint8(t.Day()))
	put711(b[3:4], uint8(t.Hour()))
	put711(b[4:5], uint8(t.Minute()))
	put711(b[5:6], uint8(t.Second()))
	put712(b[6:7], gmtOffsetQuarters(t))
}

// putDigits writes v right-justified into b as zero-padded ASCII decimal
// digits, as required by the digit fields of ECMA-119 8.4.26.1.
func putDigits(b []byte, v int) {
	for i := len(b) - 1; i >= 0; i-- {
		b[i] = byte('0' + v%10)
		v /= 10
	}
}

// sectorsFor returns the number of Logical Sectors needed to hold n bytes.
func sectorsFor(n uint64) uint32 {
	return uint32((n + LogicalSectorSize - 1) / LogicalSectorSize)
}

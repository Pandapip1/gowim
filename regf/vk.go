package regf

import "fmt"

// vkHeaderSize is the size of the fixed-length portion of a version-1.2+
// value-key (vk) cell, up to and including the padding field. See "Value
// key - version 1.2 and later".
const vkHeaderSize = 20

var vkMagic = [2]byte{'v', 'k'}

// dataSizeInlineFlag is the MSB of a vk cell's data-size field. When set,
// the data-offset field holds the value's data directly instead of a cell
// offset. See "Value key - version 1.2 and later": "If the MSB 0x80000000 of
// the data size is set the data offset actually contains the data value."
const dataSizeInlineFlag = 0x80000000

// maxInlineDataSize is the largest value-data length this package will
// inline into a vk cell's 4-byte data-offset field (a data size of 0, 1, 2,
// or 4 uses 0, the low 1, low 2, or all 4 bytes of the field respectively,
// per the spec note directly below the data-size field). This package never
// synthesizes an inline representation for length 3, since the spec only
// documents 0/1/2/4; a length-3 value is instead given its own out-of-line
// cell when built via buildValueCell (see isInlinableDataSize).
const maxInlineDataSize = 4

// isInlinableDataSize reports whether n is one of the lengths the spec
// documents as usable with the vk "data in offset" convention (0, 1, 2, 4).
func isInlinableDataSize(n int) bool {
	return n == 0 || n == 1 || n == 2 || n == 4
}

// Value data types (REG_*), from the "Data types" section.
const (
	RegNone                     uint32 = 0x00000000
	RegSZ                       uint32 = 0x00000001
	RegExpandSZ                 uint32 = 0x00000002
	RegBinary                   uint32 = 0x00000003
	RegDWORD                    uint32 = 0x00000004 // REG_DWORD_LITTLE_ENDIAN
	RegDWORDBigEndian           uint32 = 0x00000005
	RegLink                     uint32 = 0x00000006
	RegMultiSZ                  uint32 = 0x00000007
	RegResourceList             uint32 = 0x00000008
	RegFullResourceDescriptor   uint32 = 0x00000009
	RegResourceRequirementsList uint32 = 0x0000000a
	RegQWORD                    uint32 = 0x0000000b // REG_QWORD_LITTLE_ENDIAN
)

// Value key flags, from the "Value key flags" section.
const (
	ValueFlagCompName uint16 = 0x0001 // VALUE_COMP_NAME: name is ASCII, not UTF-16LE
)

// Value is one vk cell: a named, typed piece of data belonging to a Key.
type Value struct {
	// Name is the value's name, as raw on-disk bytes: ASCII if
	// Flags&ValueFlagCompName != 0, otherwise UTF-16LE. An empty Name means
	// the key's unnamed/"(default)" value, per "Value key - version 1.2 and
	// later": "If the value name size is 0 the value name is '(default)'".
	Name []byte
	// Type is the REG_* data type (see the Reg* constants).
	Type uint32
	// Flags holds the value-key flags verbatim (see ValueFlagCompName).
	Flags uint16
	// Data is the value's raw data bytes, decoded from wherever they are
	// actually stored on disk (inline in the vk cell, a single out-of-line
	// cell, or reassembled from a "db" big-data segment list). AppendTo
	// re-derives the on-disk representation (inline vs. cell vs. db) purely
	// from len(Data); see maxInlineDataSize and bigdata.go's threshold.
	Data []byte
}

// NameUTF8 decodes Name to a Go string, honoring ValueFlagCompName.
func (v Value) NameUTF8() string {
	if v.Flags&ValueFlagCompName != 0 {
		return string(v.Name)
	}
	return utf16leToString(v.Name)
}

// parseVKCell decodes a vk cell's data (the cell content, not including
// trailing padding). It does not resolve the data offset/db chain; that
// requires access to the whole hive bins area and is done by hive.go's
// resolveValueData.
func parseVKCell(data []byte) (name []byte, dataSize uint32, dataOffset uint32, typ uint32, flags uint16, err error) {
	if len(data) < vkHeaderSize {
		return nil, 0, 0, 0, 0, fmt.Errorf("vk cell too short: need %d bytes, have %d", vkHeaderSize, len(data))
	}
	if string(data[0:2]) != string(vkMagic[:]) {
		return nil, 0, 0, 0, 0, fmt.Errorf("vk cell: bad signature %q", data[0:2])
	}
	nameSize := int(le.Uint16(data[2:4]))
	dataSize = le.Uint32(data[4:8])
	dataOffset = le.Uint32(data[8:12])
	typ = le.Uint32(data[12:16])
	flags = le.Uint16(data[16:18])
	if vkHeaderSize+nameSize > len(data) {
		return nil, 0, 0, 0, 0, fmt.Errorf("vk cell: name (size %d) overruns cell", nameSize)
	}
	name = cloneBytes(data[vkHeaderSize : vkHeaderSize+nameSize])
	return name, dataSize, dataOffset, typ, flags, nil
}

// appendToVK serializes a vk cell's fixed portion and name, without padding.
// dataSize and dataOffset are the raw on-disk fields (already encoding the
// inline-data convention if applicable); see hive.go's serialization of
// Value.Data into these two fields.
func appendToVK(dst []byte, name []byte, dataSize, dataOffset, typ uint32, flags uint16) []byte {
	var hdr [vkHeaderSize]byte
	copy(hdr[0:2], vkMagic[:])
	le.PutUint16(hdr[2:4], uint16(len(name)))
	le.PutUint32(hdr[4:8], dataSize)
	le.PutUint32(hdr[8:12], dataOffset)
	le.PutUint32(hdr[12:16], typ)
	le.PutUint16(hdr[16:18], flags)
	dst = append(dst, hdr[:]...)
	dst = append(dst, name...)
	return dst
}

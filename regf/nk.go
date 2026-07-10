package regf

import "fmt"

// nkHeaderSize is the size of the fixed-length portion of a version-1.2+
// named-key (nk) cell, i.e. everything up to and including the class name
// size field. See "Named key - version 1.2 and later".
const nkHeaderSize = 76

var nkMagic = [2]byte{'n', 'k'}

// Named key flags (KEY_*), from the "Named key flags" section.
const (
	KeyFlagVolatile     uint16 = 0x0001 // KEY_IS_VOLATILE
	KeyFlagHiveExit     uint16 = 0x0002 // KEY_HIVE_EXIT: mount point of another hive
	KeyFlagHiveEntry    uint16 = 0x0004 // KEY_HIVE_ENTRY: root key of the current hive
	KeyFlagNoDelete     uint16 = 0x0008 // KEY_NO_DELETE
	KeyFlagSymLink      uint16 = 0x0010 // KEY_SYM_LINK
	KeyFlagCompName     uint16 = 0x0020 // KEY_COMP_NAME: name is ASCII, not UTF-16LE
	KeyFlagPredefHandle uint16 = 0x0040 // KEY_PREFEF_HANDLE
	KeyFlagVirtMirrored uint16 = 0x0080 // KEY_VIRT_MIRRORED
	KeyFlagVirtTarget   uint16 = 0x0100 // KEY_VIRT_TARGET
	KeyFlagVirtualStore uint16 = 0x0200 // KEY_VIRTUAL_STORE
)

// Key is one node of a hive's key tree (an nk cell plus everything it
// transitively owns), mirroring wim.DirEntry's shape.
type Key struct {
	// Flags holds the named-key flags (KeyFlag* bits) verbatim, including
	// KeyFlagCompName, which governs how Name (and ClassName, conventionally)
	// is encoded on disk. AppendTo does not infer this bit from Name's
	// content; the caller controls it explicitly, exactly as it is stored.
	Flags uint16
	// LastWritten is a Windows FILETIME (100ns ticks since 1601-01-01 UTC).
	LastWritten uint64
	// Name is the key's name, as raw on-disk bytes: ASCII if
	// Flags&KeyFlagCompName != 0, otherwise UTF-16LE. The root key
	// conventionally has an empty name.
	Name []byte
	// ClassName is the raw class name bytes (conventionally UTF-16LE; see
	// the "Class name" section), or nil if the key has none.
	ClassName []byte
	// Security is the raw self-relative security descriptor bytes owned by
	// this key's sk cell, or nil for no security cell (security key offset
	// -1). This package does not interpret the bytes (see the package doc's
	// non-goals) and does not attempt to deduplicate/share sk cells across
	// keys the way Windows does: AppendTo gives every non-nil Security its
	// own sk cell (see hive.go).
	Security []byte
	// Values holds this key's values, in on-disk values-list order.
	Values []Value
	// Subkeys holds this key's child keys. Order is preserved from the
	// on-disk subkey list on Parse; AppendTo emits them in this slice order.
	Subkeys []*Key
}

// NameUTF8 decodes Name to a Go string, honoring KeyFlagCompName.
func (k *Key) NameUTF8() string {
	if k.Flags&KeyFlagCompName != 0 {
		return string(k.Name)
	}
	return utf16leToString(k.Name)
}

// IsRoot reports whether the key is flagged as a hive's root key
// (KEY_HIVE_ENTRY).
func (k *Key) IsRoot() bool { return k.Flags&KeyFlagHiveEntry != 0 }

// nkCell is the decoded fixed-length portion of an nk cell, plus the raw
// name bytes. It is an internal staging structure; hive.go resolves the
// offsets it carries (subkeys, values, security, class name) into a Key.
type nkCell struct {
	flags                  uint16
	lastWritten            uint64
	parentOffset           uint32
	numSubkeys             uint32
	numVolatileSubkeys     uint32
	subkeysOffset          uint32
	volatileSubkeysOffset  uint32
	numValues              uint32
	valuesOffset           uint32
	securityOffset         uint32
	classNameOffset        uint32
	largestSubkeyNameSize  uint32
	largestSubkeyClassSize uint32
	largestValueNameSize   uint32
	largestValueDataSize   uint32
	unknown6               uint32
	classNameSize          uint16
	name                   []byte
}

// parseNKCell decodes an nk cell's data (the cell content, i.e. everything
// after the 4-byte cell size field, not including any trailing padding).
func parseNKCell(data []byte) (*nkCell, error) {
	if len(data) < nkHeaderSize {
		return nil, fmt.Errorf("nk cell too short: need %d bytes, have %d", nkHeaderSize, len(data))
	}
	if string(data[0:2]) != string(nkMagic[:]) {
		return nil, fmt.Errorf("nk cell: bad signature %q", data[0:2])
	}
	n := &nkCell{
		flags:                  le.Uint16(data[2:4]),
		lastWritten:            le.Uint64(data[4:12]),
		parentOffset:           le.Uint32(data[16:20]),
		numSubkeys:             le.Uint32(data[20:24]),
		numVolatileSubkeys:     le.Uint32(data[24:28]),
		subkeysOffset:          le.Uint32(data[28:32]),
		volatileSubkeysOffset:  le.Uint32(data[32:36]),
		numValues:              le.Uint32(data[36:40]),
		valuesOffset:           le.Uint32(data[40:44]),
		securityOffset:         le.Uint32(data[44:48]),
		classNameOffset:        le.Uint32(data[48:52]),
		largestSubkeyNameSize:  le.Uint32(data[52:56]),
		largestSubkeyClassSize: le.Uint32(data[56:60]),
		largestValueNameSize:   le.Uint32(data[60:64]),
		largestValueDataSize:   le.Uint32(data[64:68]),
		unknown6:               le.Uint32(data[68:72]),
	}
	nameSize := int(le.Uint16(data[72:74]))
	n.classNameSize = le.Uint16(data[74:76])
	if nkHeaderSize+nameSize > len(data) {
		return nil, fmt.Errorf("nk cell: name (size %d) overruns cell", nameSize)
	}
	n.name = cloneBytes(data[nkHeaderSize : nkHeaderSize+nameSize])
	return n, nil
}

// appendTo serializes the nk cell's fixed portion and name, without padding.
func (n *nkCell) appendTo(dst []byte) []byte {
	var hdr [nkHeaderSize]byte
	copy(hdr[0:2], nkMagic[:])
	le.PutUint16(hdr[2:4], n.flags)
	le.PutUint64(hdr[4:12], n.lastWritten)
	le.PutUint32(hdr[16:20], n.parentOffset)
	le.PutUint32(hdr[20:24], n.numSubkeys)
	le.PutUint32(hdr[24:28], n.numVolatileSubkeys)
	le.PutUint32(hdr[28:32], n.subkeysOffset)
	le.PutUint32(hdr[32:36], n.volatileSubkeysOffset)
	le.PutUint32(hdr[36:40], n.numValues)
	le.PutUint32(hdr[40:44], n.valuesOffset)
	le.PutUint32(hdr[44:48], n.securityOffset)
	le.PutUint32(hdr[48:52], n.classNameOffset)
	le.PutUint32(hdr[52:56], n.largestSubkeyNameSize)
	le.PutUint32(hdr[56:60], n.largestSubkeyClassSize)
	le.PutUint32(hdr[60:64], n.largestValueNameSize)
	le.PutUint32(hdr[64:68], n.largestValueDataSize)
	le.PutUint32(hdr[68:72], n.unknown6)
	le.PutUint16(hdr[72:74], uint16(len(n.name)))
	le.PutUint16(hdr[74:76], n.classNameSize)
	dst = append(dst, hdr[:]...)
	dst = append(dst, n.name...)
	return dst
}

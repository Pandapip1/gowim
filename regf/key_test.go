package regf

import (
	"reflect"
	"testing"
)

func TestKeySubkeyCaseInsensitive(t *testing.T) {
	child := &Key{Name: stringToUTF16LE("Services")}
	root := &Key{Subkeys: []*Key{child}}

	if got := root.Subkey("services"); got != child {
		t.Errorf("Subkey(%q) = %v, want %v", "services", got, child)
	}
	if got := root.Subkey("SERVICES"); got != child {
		t.Errorf("Subkey(%q) = %v, want %v", "SERVICES", got, child)
	}
	if got := root.Subkey("NoSuchKey"); got != nil {
		t.Errorf("Subkey(nonexistent) = %v, want nil", got)
	}
}

func TestKeyDeleteSubkey(t *testing.T) {
	a := &Key{Name: stringToUTF16LE("A")}
	b := &Key{Name: stringToUTF16LE("B")}
	root := &Key{Subkeys: []*Key{a, b}}

	if !root.DeleteSubkey("a") { // case-insensitive
		t.Fatal("DeleteSubkey(a) = false, want true")
	}
	if len(root.Subkeys) != 1 || root.Subkeys[0] != b {
		t.Fatalf("Subkeys after delete = %+v, want [B]", root.Subkeys)
	}
	if root.DeleteSubkey("A") {
		t.Error("DeleteSubkey(A) a second time = true, want false (already removed)")
	}
}

func TestKeyFindOrCreateSubkeyIdempotent(t *testing.T) {
	root := &Key{}

	first := root.FindOrCreateSubkey("Services")
	if first == nil {
		t.Fatal("FindOrCreateSubkey returned nil")
	}
	second := root.FindOrCreateSubkey("services") // case-insensitive
	if second != first {
		t.Error("FindOrCreateSubkey did not return the same *Key on a repeat call")
	}
	if len(root.Subkeys) != 1 {
		t.Errorf("len(Subkeys) = %d, want 1 (no duplicate created)", len(root.Subkeys))
	}
}

func TestKeyValueSetDeleteCaseInsensitive(t *testing.T) {
	k := &Key{}

	k.SetValue("Type", RegDWORD, EncodeDWORD(1))
	if v := k.Value("type"); v == nil {
		t.Fatal("Value(type) = nil after SetValue(Type, ...)")
	} else if got, err := v.DWORD(); err != nil || got != 1 {
		t.Errorf("DWORD() = (%d, %v), want (1, nil)", got, err)
	}

	// SetValue again with the same (differently-cased) name replaces, not
	// duplicates.
	k.SetValue("TYPE", RegDWORD, EncodeDWORD(2))
	if len(k.Values) != 1 {
		t.Fatalf("len(Values) = %d, want 1 (replaced, not duplicated)", len(k.Values))
	}
	if v := k.Value("Type"); v == nil {
		t.Fatal("Value(Type) = nil")
	} else if got, _ := v.DWORD(); got != 2 {
		t.Errorf("DWORD() = %d, want 2 (updated value)", got)
	}

	if !k.DeleteValue("tYpE") {
		t.Error("DeleteValue(tYpE) = false, want true")
	}
	if k.Value("Type") != nil {
		t.Error("Value(Type) != nil after DeleteValue")
	}
	if k.DeleteValue("Type") {
		t.Error("DeleteValue(Type) a second time = true, want false")
	}
}

func TestValueDWORDErrors(t *testing.T) {
	v := Value{Data: []byte{1, 2, 3}}
	if _, err := v.DWORD(); err == nil {
		t.Error("DWORD() on a 3-byte value: expected an error, got nil")
	}
}

func TestKeyOpenPath(t *testing.T) {
	leaf := &Key{Name: stringToUTF16LE("CurrentVersion")}
	windows := &Key{Name: stringToUTF16LE("Windows NT"), Subkeys: []*Key{leaf}}
	microsoft := &Key{Name: stringToUTF16LE("Microsoft"), Subkeys: []*Key{windows}}
	root := &Key{Subkeys: []*Key{microsoft}}

	got := root.OpenPath(`Microsoft\Windows NT\CurrentVersion`)
	if got != leaf {
		t.Errorf("OpenPath = %v, want %v", got, leaf)
	}

	// '/' separators, mixed case, and leading/trailing/repeated separators
	// are all accepted equivalently.
	if got := root.OpenPath(`/microsoft/WINDOWS NT/currentversion/`); got != leaf {
		t.Errorf("OpenPath (alt separators/case) = %v, want %v", got, leaf)
	}

	if got := root.OpenPath(""); got != root {
		t.Errorf("OpenPath(\"\") = %v, want root itself (%v)", got, root)
	}

	if got := root.OpenPath(`Microsoft\NoSuchKey`); got != nil {
		t.Errorf("OpenPath(missing component) = %v, want nil", got)
	}
	if got := root.OpenPath(`Microsoft\Windows NT\CurrentVersion\TooDeep`); got != nil {
		t.Errorf("OpenPath(path past a leaf) = %v, want nil", got)
	}
}

func TestKeyFindOrCreatePath(t *testing.T) {
	root := &Key{}

	first := root.FindOrCreatePath(`Microsoft\Windows\CurrentVersion`)
	if first == nil {
		t.Fatal("FindOrCreatePath returned nil")
	}
	first.SetValue("Marker", RegDWORD, EncodeDWORD(42))

	// Calling it again with the same path returns the SAME key (does not
	// duplicate the intermediate Microsoft/Windows subkeys), matching
	// FindOrCreateSubkey's own idempotence.
	second := root.FindOrCreatePath(`Microsoft\Windows\CurrentVersion`)
	if second != first {
		t.Error("FindOrCreatePath did not return the same *Key on a repeat call")
	}
	if v := second.Value("Marker"); v == nil {
		t.Error("Marker value lost across repeat FindOrCreatePath call")
	}

	if got := root.FindOrCreatePath(""); got != root {
		t.Errorf("FindOrCreatePath(\"\") = %v, want root itself (%v)", got, root)
	}
}

func TestKeyDeletePath(t *testing.T) {
	root := &Key{}
	target := root.FindOrCreatePath(`Microsoft\Windows\ToRemove`)
	target.SetValue("X", RegDWORD, EncodeDWORD(1))

	if !root.DeletePath(`Microsoft\Windows\ToRemove`) {
		t.Fatal("DeletePath = false, want true")
	}
	if root.OpenPath(`Microsoft\Windows\ToRemove`) != nil {
		t.Error("subtree still reachable after DeletePath")
	}
	// The parent path itself must still exist -- only the leaf subtree is
	// removed.
	if root.OpenPath(`Microsoft\Windows`) == nil {
		t.Error("DeletePath removed more than just the named subtree")
	}

	if root.DeletePath(`Microsoft\Windows\ToRemove`) {
		t.Error("DeletePath a second time = true, want false (already removed)")
	}
	if root.DeletePath(`NoSuchTopLevel\Sub`) {
		t.Error("DeletePath with a missing parent component = true, want false")
	}
	if root.DeletePath("") {
		t.Error(`DeletePath("") = true, want false (no leaf component to delete)`)
	}
}

func TestValueSZRoundTrip(t *testing.T) {
	v := Value{Data: EncodeSZ(`\SystemRoot\System32\drivers\contoso.sys`)}
	if got := v.SZ(); got != `\SystemRoot\System32\drivers\contoso.sys` {
		t.Errorf("SZ() = %q", got)
	}
}

func TestValueMultiSZRoundTrip(t *testing.T) {
	strs := []string{"NetBIOSGroup", "RpcSs"}
	v := Value{Data: EncodeMultiSZ(strs)}
	if got := v.MultiSZ(); !reflect.DeepEqual(got, strs) {
		t.Errorf("MultiSZ() = %+v, want %+v", got, strs)
	}

	empty := Value{}
	if got := empty.MultiSZ(); got != nil {
		t.Errorf("MultiSZ() on empty data = %+v, want nil", got)
	}
}

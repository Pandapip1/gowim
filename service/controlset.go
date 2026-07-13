package service

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/Pandapip1/gowim/regf"
)

// CurrentControlSet resolves a SYSTEM hive's authoritative control set:
// HKEY_LOCAL_MACHINE\SYSTEM\Select\Default (a REG_DWORD) names the number N
// of the "ControlSetNNN" (zero-padded to 3 digits) subkey that is
// authoritative, and CurrentControlSet finds and returns that subkey of
// systemRoot.
//
// This is standard, well-documented Windows registry structure - see e.g.
// the winreg-kb project's "Current control set" reference,
// https://winreg-kb.readthedocs.io/en/latest/sources/system-keys/Current-control-set.html:
// "Under HKEY_LOCAL_MACHINE\System\Select, four REG_DWORD values define
// control set roles: Current..., Default..., Failed..., LastKnownGood...",
// and "CurrentControlSet exists only at runtime; it's not a persistent
// registry key... the numeric value stored in the Current entry determines
// which physical ControlSet key (001, 002, etc.) is active" - i.e.
// CurrentControlSet is conventionally a registry symbolic link resolved live
// by the kernel, not a real subtree an offline hive-editing tool should try
// to open directly; Select\Default is the on-disk source of truth this
// function reads instead. (Default, rather than Current, is used because
// Current only reflects the running system's live choice - not present or
// meaningful in an offline image - whereas Default is what the system will
// boot into next and is always present on disk.)
//
// This is the natural utility a caller of this package needs to find where
// to put the Services key (e.g.
// currentControlSet.FindOrCreateSubkey("Services")) - it is generic
// SYSTEM-hive knowledge, not specific to services, but is included here
// (rather than in a separate module) so this package is usable entirely on
// its own.
func CurrentControlSet(systemRoot *regf.Key) (*regf.Key, error) {
	if systemRoot == nil {
		return nil, wrapErr("current control set", errors.New("nil SYSTEM hive root key"))
	}

	selectKey := systemRoot.Subkey("Select")
	if selectKey == nil {
		return nil, wrapErr("current control set", errors.New(`no "Select" subkey under SYSTEM hive root`))
	}

	defVal := selectKey.Value("Default")
	if defVal == nil {
		return nil, wrapErr("current control set", errors.New(`no "Default" value under "Select"`))
	}
	if len(defVal.Data) < 4 {
		return nil, wrapErr("current control set", fmt.Errorf(`"Select\Default" value has %d bytes, want at least 4 (REG_DWORD)`, len(defVal.Data)))
	}
	n := binary.LittleEndian.Uint32(defVal.Data[:4])

	name := fmt.Sprintf("ControlSet%03d", n)
	cs := systemRoot.Subkey(name)
	if cs == nil {
		return nil, wrapErr("current control set", fmt.Errorf("no %q subkey under SYSTEM hive root", name))
	}
	return cs, nil
}

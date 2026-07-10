// This file extends the driver package to write a driver package's Services
// and CriticalDeviceDatabase registry registration into a SYSTEM-hive-shaped
// *regf.Key tree, using the sibling regf package
// (github.com/gavin-john/gowim/regf) purely as a plain Key/Value struct tree
// - exactly as install.go builds *wim.DirEntry trees by hand with no special
// "tree API" from the wim package. findSubkey/findOrCreateSubkey/findValue/
// setValue below are this package's own local navigation helpers on top of
// regf.Key/regf.Value; nothing is added to the regf package itself.
//
// Explicit non-goals (in addition to those in driver.go's package doc):
//
//   - The DriverDatabase key tree (SYSTEM\DriverDatabase\DriverPackages,
//     DeviceIds, etc.), the internal driver-ranking database PnP uses to
//     choose among multiple matching drivers for a device. This was checked,
//     not just assumed: a Microsoft Learn documentation search turned up no
//     authoritative page describing DriverDatabase's subkey/value schema
//     (unlike CriticalDeviceDatabase and the Services key schema, both of
//     which are documented - see criticaldevicedatabase.go and service.go);
//     the only discussion found was community speculation (e.g. a Microsoft
//     Q&A/TechNet thread describing it as "redundantly duplicat[ing]"
//     FileRepository metadata, with no cited schema). Reproducing it would
//     mean reverse-engineering undocumented internals, which is out of scope
//     here - the same policy driver.go's DriverStore-hash non-goal states.
//   - The Enum device-instance tree
//     (SYSTEM\CurrentControlSet\Enum\<enumerator>\<device-id>\<instance-id>).
//     Registering an actual device instance there requires a real, live
//     hardware instance ID discovered by PnP enumeration at boot/setup time,
//     which an offline image-prep tool run against a driver package alone
//     does not have.
//   - INFCACHE.1 (the binary cache of parsed INF metadata SetupAPI maintains
//     under %SystemRoot%\INF): a distinct, undocumented binary format in its
//     own right.
//   - AddReg/DelReg directives in general, beyond the specific Services
//     (Type/Start/ErrorControl/ImagePath/Group/DependOnGroup/
//     DependOnService) and CriticalDeviceDatabase (ClassGUID/Service) values
//     this file writes: a generic, much broader directive mechanism this
//     package does not take on.
//   - Registry-hive writing back to disk (deciding which hive file to open,
//     backing it up, replacing it): InstallRegistry only produces/merges
//     regf.Key/regf.Value structures given an already-loaded *regf.Hive's (or
//     freshly-built) Key tree; the caller handles file I/O, exactly as
//     Install's wim-side counterpart only returns in-memory nodes rather than
//     writing a WIM file itself.
package driver

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/gavin-john/gowim/regf"
)

// findSubkey returns the direct child of key named name (matching Windows'
// case-insensitive registry namespace), or nil if none exists.
func findSubkey(key *regf.Key, name string) *regf.Key {
	for _, k := range key.Subkeys {
		if strings.EqualFold(k.NameUTF8(), name) {
			return k
		}
	}
	return nil
}

// findOrCreateSubkey returns the direct child of key named name, creating it
// (as a plain, non-root, UTF-16LE-named key with no values/subkeys of its
// own yet) if it does not already exist. Calling it twice with the same name
// returns the same *regf.Key both times, which is what makes InstallRegistry
// safe to call repeatedly without duplicating subkeys.
func findOrCreateSubkey(key *regf.Key, name string) *regf.Key {
	if child := findSubkey(key, name); child != nil {
		return child
	}
	child := &regf.Key{Name: stringToUTF16LE(name)}
	key.Subkeys = append(key.Subkeys, child)
	return child
}

// findValue returns a pointer to key's value named name (matching Windows'
// case-insensitive registry namespace), or nil if none exists.
func findValue(key *regf.Key, name string) *regf.Value {
	for i := range key.Values {
		if strings.EqualFold(key.Values[i].NameUTF8(), name) {
			return &key.Values[i]
		}
	}
	return nil
}

// setValue creates or overwrites key's value named name with the given type
// and data, so that calling it twice with the same name replaces rather than
// duplicates the value - the value-side half of what makes InstallRegistry
// idempotent.
func setValue(key *regf.Key, name string, typ uint32, data []byte) {
	if v := findValue(key, name); v != nil {
		v.Type = typ
		v.Data = data
		return
	}
	key.Values = append(key.Values, regf.Value{Name: stringToUTF16LE(name), Type: typ, Data: data})
}

// uint32LEBytes little-endian-encodes v as a 4-byte REG_DWORD value.
func uint32LEBytes(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

// multiSZBytes encodes strs as a REG_MULTI_SZ value: each string as
// UTF-16LE followed by a UTF-16 NUL terminator, with the whole list
// terminated by one further (empty-string) NUL.
func multiSZBytes(strs []string) []byte {
	var out []byte
	for _, s := range strs {
		out = append(out, stringToUTF16LE(s)...)
		out = append(out, 0, 0)
	}
	out = append(out, 0, 0)
	return out
}

// mergeServiceInstall produces/merges a Services\<svc.Name> subkey under
// servicesKey with the well-known service registration values - see
// "HKLM\SYSTEM\CurrentControlSet\Services Registry Tree",
// https://learn.microsoft.com/windows-hardware/drivers/install/hklm-system-currentcontrolset-services-registry-tree
// (Start/Type/ErrorControl/ImagePath), corroborated for ImagePath's
// REG_EXPAND_SZ type by that page's own text ("Windows creates this value by
// using the required ServiceBinary entry") together with real-world driver
// service registrations, which consistently record ImagePath as
// REG_EXPAND_SZ (e.g. "\SystemRoot\System32\drivers\<name>.sys", which must
// expand %SystemRoot%) rather than REG_SZ. Group/DependOnGroup/
// DependOnService are not on that page but are the well-known SCM service
// key value names for LoadOrderGroup/Dependencies (see
// SERVICE_FAILURE_ACTIONS-adjacent SCM documentation and
// CreateService/QueryServiceConfig's lpDependencies semantics, which the
// AddService directive's Dependencies entry feeds).
//
// destDirs supplies the destination directory path for svc.BinaryDirID, in
// the same shape Install (install.go) already takes for payload files, so a
// single destDirs map serves both file placement and ImagePath computation.
func mergeServiceInstall(servicesKey *regf.Key, svc ServiceInstall, destDirs map[DirID]string) error {
	if svc.Name == "" {
		return wrapErr("install registry", errors.New("service has no name"))
	}

	base, ok := destDirs[svc.BinaryDirID]
	if !ok {
		return wrapErr("install registry", fmt.Errorf(
			"no destination directory supplied for DIRID %d (service %s)", svc.BinaryDirID, svc.Name))
	}

	key := findOrCreateSubkey(servicesKey, svc.Name)

	setValue(key, "Type", regf.RegDWORD, uint32LEBytes(svc.ServiceType))
	setValue(key, "Start", regf.RegDWORD, uint32LEBytes(svc.StartType))
	setValue(key, "ErrorControl", regf.RegDWORD, uint32LEBytes(svc.ErrorControl))

	components := pathComponents(base, svc.BinaryPath)
	if len(components) == 0 {
		return wrapErr("install registry", fmt.Errorf("empty ImagePath for service %s", svc.Name))
	}
	imagePath := `\` + strings.Join(components, `\`)
	setValue(key, "ImagePath", regf.RegExpandSZ, stringToUTF16LE(imagePath))

	if svc.LoadOrderGroup != "" {
		setValue(key, "Group", regf.RegSZ, stringToUTF16LE(svc.LoadOrderGroup))
	} else {
		removeValue(key, "Group")
	}
	if len(svc.DependOnGroup) > 0 {
		setValue(key, "DependOnGroup", regf.RegMultiSZ, multiSZBytes(svc.DependOnGroup))
	} else {
		removeValue(key, "DependOnGroup")
	}
	if len(svc.DependOnService) > 0 {
		setValue(key, "DependOnService", regf.RegMultiSZ, multiSZBytes(svc.DependOnService))
	} else {
		removeValue(key, "DependOnService")
	}

	return nil
}

// removeValue deletes key's value named name (if present), keeping
// mergeServiceInstall's re-installation of a package whose optional fields
// changed (or were removed) from leaving stale values behind.
func removeValue(key *regf.Key, name string) {
	for i := range key.Values {
		if strings.EqualFold(key.Values[i].NameUTF8(), name) {
			key.Values = append(key.Values[:i], key.Values[i+1:]...)
			return
		}
	}
}

// mergeCriticalDeviceDatabase produces/merges one subkey per entry under
// cddbKey (the caller-supplied CriticalDeviceDatabase key itself), named per
// CriticalDeviceDatabaseSubkeyName, with the ClassGUID/Service values - see
// the CriticalDeviceDatabaseEntry doc comment in criticaldevicedatabase.go
// for citations.
func mergeCriticalDeviceDatabase(cddbKey *regf.Key, entries []CriticalDeviceDatabaseEntry) {
	for _, e := range entries {
		key := findOrCreateSubkey(cddbKey, CriticalDeviceDatabaseSubkeyName(e.HardwareID))
		if e.ClassGuid != "" {
			setValue(key, "ClassGUID", regf.RegSZ, stringToUTF16LE(e.ClassGuid))
		}
		if e.Service != "" {
			setValue(key, "Service", regf.RegSZ, stringToUTF16LE(e.Service))
		}
	}
}

// InstallRegistry merges pkg's Services and CriticalDeviceDatabase registry
// registration (see service.go's Services and criticaldevicedatabase.go's
// CriticalDeviceDatabaseEntries) into currentControlSet, the root of the
// SYSTEM hive's current control set (e.g. as resolved by CurrentControlSet).
// It creates/reuses a "Services" subkey directly under currentControlSet,
// and a "CriticalDeviceDatabase" subkey under currentControlSet's "Control"
// subkey, matching the documented
// HKLM\SYSTEM\CurrentControlSet\Services / Control\CriticalDeviceDatabase
// layout.
//
// destDirs has the same shape and meaning as Install's destDirs parameter
// (install.go): it maps each DIRID used by any service's ServiceBinary entry
// to the image-relative directory path that DIRID resolves to. It is an
// error for a service's ServiceBinary DIRID to be absent from destDirs.
//
// Like Install, InstallRegistry is safe to call more than once with the
// same package against the same tree: findOrCreateSubkey/setValue
// find-or-create rather than blindly append, so a second call updates
// (rather than duplicates) the same Services\<name> and
// CriticalDeviceDatabase\<hwid> subkeys/values.
func InstallRegistry(currentControlSet *regf.Key, pkg *Package, destDirs map[DirID]string, platform string) error {
	if currentControlSet == nil {
		return wrapErr("install registry", errors.New("nil current control set key"))
	}
	if pkg == nil {
		return wrapErr("install registry", errors.New("nil package"))
	}

	services, err := pkg.Services(platform)
	if err != nil {
		return err
	}
	cdEntries, err := pkg.CriticalDeviceDatabaseEntries(platform)
	if err != nil {
		return err
	}

	servicesKey := findOrCreateSubkey(currentControlSet, "Services")
	for _, svc := range services {
		if err := mergeServiceInstall(servicesKey, svc, destDirs); err != nil {
			return err
		}
	}

	controlKey := findOrCreateSubkey(currentControlSet, "Control")
	cddbKey := findOrCreateSubkey(controlKey, "CriticalDeviceDatabase")
	mergeCriticalDeviceDatabase(cddbKey, cdEntries)

	return nil
}

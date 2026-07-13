package service

import (
	"errors"
	"fmt"

	"github.com/Pandapip1/gowim/regf"
)

// Install produces/merges a Services\<svc.Name> subkey under servicesKey
// (which the caller has already navigated/created, e.g. via
// CurrentControlSet plus currentControlSet.FindOrCreateSubkey("Services"))
// with the well-known service registration values - see "HKLM\SYSTEM\
// CurrentControlSet\Services Registry Tree",
// https://learn.microsoft.com/windows-hardware/drivers/install/hklm-system-currentcontrolset-services-registry-tree
// (Start/Type/ErrorControl/ImagePath), corroborated for ImagePath's
// REG_EXPAND_SZ type by that page's own text ("Windows creates this value by
// using the required ServiceBinary entry") together with real-world driver
// service registrations, which consistently record ImagePath as
// REG_EXPAND_SZ (e.g. "\SystemRoot\System32\drivers\<name>.sys", which must
// expand %SystemRoot%) rather than REG_SZ. Group/DependOnGroup/
// DependOnService are not on that page but are the well-known SCM service
// key value names for CreateService's lpLoadOrderGroup/lpDependencies
// parameters - see "CreateServiceW function (winsvc.h)",
// https://learn.microsoft.com/windows/win32/api/winsvc/nf-winsvc-createservicew,
// whose Remarks section's value table lists Group, DependOnGroup, and
// DependOnService alongside Type/Start/ErrorControl/ImagePath as the values
// CreateService is documented to persist under this same Services\<name>
// key. DisplayName/ObjectName are corroborated by "ChangeServiceConfigW
// function (winsvc.h)",
// https://learn.microsoft.com/windows/win32/api/winsvc/nf-winsvc-changeserviceconfigw
// (the lpDisplayName/lpServiceStartName parameter sections), and Description
// by "SERVICE_DESCRIPTIONW (winsvc.h)",
// https://learn.microsoft.com/windows/win32/api/winsvc/ns-winsvc-service_descriptionw
// (the lpDescription member) - see Service's field doc comments for the
// precise quoted text.
//
// Install is safe to call more than once with the same svc.Name against the
// same tree: it finds-or-creates rather than blindly appending, so a second
// call updates (rather than duplicates) the same Services\<name> subkey and
// its values, and clears (rather than leaves stale) any of Group/
// DisplayName/Description/ObjectName/DependOnGroup/DependOnService that svc
// no longer sets.
func Install(servicesKey *regf.Key, svc Service) error {
	if servicesKey == nil {
		return wrapErr("install", errors.New("nil services key"))
	}
	if err := validateService(svc); err != nil {
		return wrapErr("install", err)
	}

	key := servicesKey.FindOrCreateSubkey(svc.Name)
	writeServiceValues(key, svc)
	return nil
}

// validateService checks the invariants both Install and Modify require of
// svc before they touch the registry tree at all.
func validateService(svc Service) error {
	if svc.Name == "" {
		return errors.New("service has no name")
	}
	if svc.ImagePath == "" {
		return fmt.Errorf("service %s has no ImagePath", svc.Name)
	}
	return nil
}

// writeServiceValues writes svc's values into key, the already
// found-or-created Services\<svc.Name> subkey, clearing any optional value
// svc does not set. It is the single value-writing implementation shared by
// Install (find-or-create) and Modify (must already exist) so the two never
// duplicate this logic.
func writeServiceValues(key *regf.Key, svc Service) {
	key.SetValue("Type", regf.RegDWORD, regf.EncodeDWORD(svc.Type))
	key.SetValue("Start", regf.RegDWORD, regf.EncodeDWORD(svc.Start))
	key.SetValue("ErrorControl", regf.RegDWORD, regf.EncodeDWORD(svc.ErrorControl))
	key.SetValue("ImagePath", regf.RegExpandSZ, regf.EncodeSZ(svc.ImagePath))

	setOrRemoveSZ(key, "Group", svc.Group)
	setOrRemoveSZ(key, "DisplayName", svc.DisplayName)
	setOrRemoveSZ(key, "Description", svc.Description)
	setOrRemoveSZ(key, "ObjectName", svc.ObjectName)

	if len(svc.DependOnGroup) > 0 {
		key.SetValue("DependOnGroup", regf.RegMultiSZ, regf.EncodeMultiSZ(svc.DependOnGroup))
	} else {
		key.DeleteValue("DependOnGroup")
	}
	if len(svc.DependOnService) > 0 {
		key.SetValue("DependOnService", regf.RegMultiSZ, regf.EncodeMultiSZ(svc.DependOnService))
	} else {
		key.DeleteValue("DependOnService")
	}
}

// setOrRemoveSZ sets key's REG_SZ value named name to value if value is
// non-empty, or removes it if value is "" - the shared "optional string
// field" pattern Install/Modify use for Group/DisplayName/Description/
// ObjectName.
func setOrRemoveSZ(key *regf.Key, name, value string) {
	if value != "" {
		key.SetValue(name, regf.RegSZ, regf.EncodeSZ(value))
	} else {
		key.DeleteValue(name)
	}
}

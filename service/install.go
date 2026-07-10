package service

import (
	"errors"
	"fmt"

	"github.com/Pandapip1/gowim/regf"
)

// Install produces/merges a Services\<svc.Name> subkey under servicesKey
// (which the caller has already navigated/created, e.g. via
// CurrentControlSet plus FindOrCreateSubkey(currentControlSet, "Services"))
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

	key := FindOrCreateSubkey(servicesKey, svc.Name)
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
	SetValue(key, "Type", regf.RegDWORD, uint32LEBytes(svc.Type))
	SetValue(key, "Start", regf.RegDWORD, uint32LEBytes(svc.Start))
	SetValue(key, "ErrorControl", regf.RegDWORD, uint32LEBytes(svc.ErrorControl))
	SetValue(key, "ImagePath", regf.RegExpandSZ, stringToUTF16LE(svc.ImagePath))

	setOrRemoveSZ(key, "Group", svc.Group)
	setOrRemoveSZ(key, "DisplayName", svc.DisplayName)
	setOrRemoveSZ(key, "Description", svc.Description)
	setOrRemoveSZ(key, "ObjectName", svc.ObjectName)

	if len(svc.DependOnGroup) > 0 {
		SetValue(key, "DependOnGroup", regf.RegMultiSZ, multiSZBytes(svc.DependOnGroup))
	} else {
		RemoveValue(key, "DependOnGroup")
	}
	if len(svc.DependOnService) > 0 {
		SetValue(key, "DependOnService", regf.RegMultiSZ, multiSZBytes(svc.DependOnService))
	} else {
		RemoveValue(key, "DependOnService")
	}
}

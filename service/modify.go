package service

import (
	"errors"
	"fmt"

	"github.com/Pandapip1/gowim/regf"
)

// Modify updates an existing Services\<svc.Name> subkey under servicesKey
// with svc's values, using exactly the same value-writing logic as Install
// (see writeServiceValues in install.go) - including clearing any of
// Group/DisplayName/Description/ObjectName/DependOnGroup/DependOnService
// that svc no longer sets.
//
// Unlike Install, Modify requires the subkey to already exist: it looks it
// up via FindSubkey rather than creating it, and returns an error wrapping
// ErrNotFound (checkable with errors.Is) if svc.Name has no existing
// Services subkey, rather than silently creating one. This lets callers
// distinguish "install a new service" from "reconfigure one that should
// already be there" and fail loudly on a typo'd/missing name.
func Modify(servicesKey *regf.Key, svc Service) error {
	if servicesKey == nil {
		return wrapErr("modify", errors.New("nil services key"))
	}
	if err := validateService(svc); err != nil {
		return wrapErr("modify", err)
	}

	key := FindSubkey(servicesKey, svc.Name)
	if key == nil {
		return wrapErr("modify", fmt.Errorf("service %q: %w", svc.Name, ErrNotFound))
	}

	writeServiceValues(key, svc)
	return nil
}

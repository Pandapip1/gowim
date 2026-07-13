package service

import (
	"errors"
	"fmt"

	"github.com/Pandapip1/gowim/regf"
)

// Delete removes the Services\<name> subkey (and everything under it)
// entirely from servicesKey. It returns an error wrapping ErrNotFound
// (checkable with errors.Is) if no such subkey exists, rather than treating
// a missing/typo'd name as a silent no-op - deleting a service that was
// never there, or deleting it twice, is a caller error worth surfacing.
func Delete(servicesKey *regf.Key, name string) error {
	if servicesKey == nil {
		return wrapErr("delete", errors.New("nil services key"))
	}
	if name == "" {
		return wrapErr("delete", errors.New("no service name given"))
	}

	if !servicesKey.DeleteSubkey(name) {
		return wrapErr("delete", fmt.Errorf("service %q: %w", name, ErrNotFound))
	}
	return nil
}

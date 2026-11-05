package gitread

import (
	"errors"
	"path/filepath"
)

// Resource paths come from the control plane's persisted canonical approval,
// not from a new user path selection. Never EvalSymlinks here: doing so would
// silently grant a replacement link's target the old Resource's authority.
func canonicalResourceDirectory(value string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", errors.New("resource path must be absolute")
	}
	cleaned, err := normalizeTrustedResourceAlias(filepath.Clean(value))
	if err != nil {
		return "", err
	}
	identity, err := captureStablePathIdentity(cleaned)
	if err != nil {
		return "", err
	}
	defer identity.close()
	if err := identity.validate(); err != nil {
		return "", err
	}
	return cleaned, nil
}

func resourceIdentityHash(identity stablePathIdentity) (string, error) {
	values := []string{identity.path}
	for _, component := range identity.components {
		stable, err := platformStableFileIdentity(component.path)
		if err != nil {
			return "", err
		}
		values = append(values, component.path, stable)
	}
	if err := identity.validate(); err != nil {
		return "", err
	}
	return stableSHA256ID("resource-identity", values...), nil
}

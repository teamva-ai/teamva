//go:build !darwin

package gitread

func normalizeTrustedResourceAlias(value string) (string, error) { return value, nil }

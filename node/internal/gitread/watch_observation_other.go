//go:build !darwin && !linux

package gitread

import "os"

// Git sandbox reads are unsupported on the remaining platforms. Refuse to
// suppress attribute events there rather than relying on an incomplete file
// identity.
func platformWatchIdentity(os.FileInfo) (string, bool) { return "", false }

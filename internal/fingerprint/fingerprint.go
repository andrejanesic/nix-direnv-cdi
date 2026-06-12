// Package fingerprint derives a stable, CDI-device-name-valid identifier for a
// project root. It is used as the CDI device name in shared placement mode so
// that a single registered spec dir can host many projects. (PLAN §2.)
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

// Length is the number of hex characters in a fingerprint. 16 hex chars
// (64 bits) is ample to avoid collisions across a user's projects while keeping
// the device reference short.
const Length = 16

// Compute returns the fingerprint of a project root: the hex SHA-256 of the
// cleaned path, truncated to Length. It is deterministic and stable, distinct
// roots yield distinct ids, and the result is a valid CDI device name (lower
// hex contains no slashes, '=', or other reserved characters).
func Compute(projectRoot string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(projectRoot)))
	return hex.EncodeToString(sum[:])[:Length]
}

//go:build !darwin && !linux && !windows

package pi

import "os"

// snapshotWorkspaceRoot retains Go validation on platforms without a verified
// Node identity mapping. Empty proof fields preserve human approval for reads.
func snapshotWorkspaceRoot(path string) (os.FileInfo, string, string, error) {
	info, err := os.Stat(path)
	return info, "", "", err
}

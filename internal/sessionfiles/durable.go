// Package sessionfiles provides filesystem durability for native session records.
package sessionfiles

import "os"

// SyncDirectory persists changes to the directory entries.
func SyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

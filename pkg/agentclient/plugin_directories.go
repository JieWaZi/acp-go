package agentclient

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// PhysicalPluginDirectories freezes local plugin roots and excludes launcher arguments
// that could introduce a second, unowned source of plugin capabilities.
func PhysicalPluginDirectories(directories, prefix []string) ([]string, error) {
	for _, arg := range prefix {
		flag, _, _ := strings.Cut(arg, "=")
		if flag == "--plugin-dir" {
			return nil, errors.New("plugin directory must use the typed launch option")
		}
	}
	result := make([]string, 0, len(directories))
	seen := map[string]bool{}
	for _, directory := range directories {
		if !filepath.IsAbs(directory) {
			return nil, errors.New("plugin directory must be absolute")
		}
		path, err := filepath.EvalSymlinks(directory)
		if err != nil {
			return nil, errors.New("plugin directory is unavailable")
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() || seen[path] {
			return nil, errors.New("invalid or duplicate plugin directory")
		}
		result = append(result, path)
		seen[path] = true
	}
	return result, nil
}

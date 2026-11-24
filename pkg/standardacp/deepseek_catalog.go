package standardacp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// PrepareDeepSeekModelCatalog isolates the native session/new used for model
// discovery while keeping the caller's selected DSH_HOME and provider settings.
// The caller owns the returned cleanup and must run it after the process exits.
func PrepareDeepSeekModelCatalog() (string, func() error, error) {
	root, err := os.MkdirTemp("", "acp-go-dsh-catalog-")
	if err != nil {
		return "", nil, errors.New("cannot allocate the DeepSeek model catalog overlay")
	}
	cleanup := func() error { return os.RemoveAll(root) }
	path := filepath.Join(root, "catalog.json")
	patch, err := json.Marshal([]map[string]any{
		{"id": "session-persistence-jsonl", "config": map[string]string{"root": filepath.Join(root, "sessions")}},
		{"id": "session-projection-cache", "disabled": true},
	})
	if err != nil || os.WriteFile(path, patch, 0600) != nil {
		return "", nil, errors.Join(errors.New("cannot write the DeepSeek model catalog overlay"), cleanup())
	}
	return path, cleanup, nil
}

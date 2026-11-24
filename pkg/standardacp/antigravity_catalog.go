package standardacp

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// PrepareAntigravityModelCatalog snapshots the selected login and model
// settings. The caller removes the private profile after the ACP process exits.
func PrepareAntigravityModelCatalog(configHome string) (string, error) {
	if !filepath.IsAbs(configHome) {
		return "", errors.New("Antigravity model discovery requires a selected profile")
	}
	dir, err := os.MkdirTemp("", "acp-go-agy-catalog-")
	if err != nil {
		return "", err
	}
	if err := copyAntigravityCatalogInputs(configHome, dir); err != nil {
		return "", errors.Join(err, os.RemoveAll(dir))
	}
	return dir, nil
}

func copyAntigravityCatalogInputs(configHome, dir string) error {
	source, err := os.OpenRoot(configHome)
	if errors.Is(err, fs.ErrNotExist) {
		// API key and Application Default Credentials can exist outside this root.
		return nil
	}
	if err != nil {
		return err
	}
	defer source.Close()

	budget := int64(8 << 20)
	for _, name := range []string{
		"google_accounts.json", "jetski-standalone-oauth-token", "settings.json",
		filepath.Join("config", "config.json"), filepath.Join("antigravity-cli", "settings.json"),
	} {
		info, err := source.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > budget {
			return errors.New("Antigravity catalog input is not a bounded regular file")
		}
		file, err := source.Open(name)
		if err != nil {
			return err
		}
		current, err := file.Stat()
		if err != nil || !os.SameFile(info, current) {
			_ = file.Close()
			return errors.New("Antigravity catalog input changed during preparation")
		}
		data, err := io.ReadAll(io.LimitReader(file, budget+1))
		_ = file.Close()
		if err == nil && int64(len(data)) > budget {
			err = errors.New("Antigravity catalog input exceeds the copy limit")
		}
		if err == nil {
			target := filepath.Join(dir, name)
			err = os.MkdirAll(filepath.Dir(target), 0700)
			if err == nil {
				err = os.WriteFile(target, data, 0600)
			}
		}
		budget -= int64(len(data))
		clear(data)
		if err != nil {
			return err
		}
	}
	return nil
}

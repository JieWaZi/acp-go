package workbuddy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

// ReadModels extracts credit multipliers from native CodeBuddy model metadata.
func (c *Client) ReadModels(ctx context.Context) (acpmeta.ModelCatalog, error) {
	catalog, err := c.Client.ReadModels(ctx)
	if err != nil {
		return catalog, err
	}
	project := func(rows []acp.SessionConfigSelectOption) {
		for i := range rows {
			rows[i].Meta = withModelCredits(rows[i].Meta)
		}
	}
	if catalog.Options.Ungrouped != nil {
		project(*catalog.Options.Ungrouped)
	}
	if catalog.Options.Grouped != nil {
		for _, group := range *catalog.Options.Grouped {
			project(group.Options)
		}
	}
	return catalog, nil
}

// HandleExtensionMethod shares one catalog between public ACP model reads and direct calls.
func (c *Client) HandleExtensionMethod(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if method == acpmeta.ModelCatalogMethod {
		return c.ReadModels(ctx)
	}
	return c.Client.HandleExtensionMethod(ctx, method, raw)
}

func withModelCredits(meta map[string]any) map[string]any {
	label, _ := meta["credits"].(string)
	number, ok := strings.CutPrefix(strings.TrimSpace(label), "x")
	if !ok {
		return meta
	}
	number, ok = strings.CutSuffix(number, " credits")
	if !ok {
		return meta
	}
	value, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return meta
	}
	return acpmeta.WithModelCreditMultiplier(meta, value)
}

// PrepareModelCatalog copies the native account inputs used by the CodeBuddy
// catalog into a private, disposable root. The caller owns removal after exit.
func PrepareModelCatalog(configHome string) (string, error) {
	dir, err := os.MkdirTemp("", "neux-codebuddy-catalog-")
	if err != nil {
		return "", err
	}
	if err := copyCatalogInputs(configHome, dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func copyCatalogInputs(configHome, dir string) error {
	if configHome == "" {
		return errors.New("CodeBuddy catalog requires an account configuration root")
	}
	source, err := os.OpenRoot(configHome)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer source.Close()
	files := []string{"settings.json", "user-state.json"}
	if info, err := source.Lstat("local_storage"); err == nil && info.IsDir() {
		store, err := source.Open("local_storage")
		if err != nil {
			return err
		}
		rows, err := store.ReadDir(129)
		_ = store.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(rows) > 128 {
			return errors.New("CodeBuddy catalog local storage exceeds the copy limit")
		}
		if err := os.Mkdir(filepath.Join(dir, "local_storage"), 0700); err != nil {
			return err
		}
		for _, row := range rows {
			if strings.HasPrefix(row.Name(), "entry_") && strings.HasSuffix(row.Name(), ".info") {
				files = append(files, filepath.Join("local_storage", row.Name()))
			}
		}
	} else if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return errors.New("CodeBuddy catalog local storage is not a regular directory")
	}
	budget := int64(8 << 20)
	for _, name := range files {
		info, err := source.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > budget {
			return errors.New("CodeBuddy catalog input is not a bounded regular file")
		}
		file, err := source.Open(name)
		if err != nil {
			return err
		}
		current, err := file.Stat()
		if err != nil || !os.SameFile(info, current) {
			_ = file.Close()
			return errors.New("CodeBuddy catalog input changed during preparation")
		}
		data, err := io.ReadAll(io.LimitReader(file, budget+1))
		_ = file.Close()
		if err != nil || int64(len(data)) > budget {
			return errors.New("CodeBuddy catalog input exceeds the copy limit")
		}
		err = os.WriteFile(filepath.Join(dir, name), data, 0600)
		budget -= int64(len(data))
		clear(data)
		if err != nil {
			return err
		}
	}
	return nil
}

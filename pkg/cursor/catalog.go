package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpclient"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
)

func decodeModelCatalog(raw json.RawMessage) (acpmeta.ModelCatalog, error) {
	var reply struct {
		// Models retains the native ordered models and their optional configuration.
		Models []struct {
			// The standard option carries the native model identity and label.
			acp.SessionConfigSelectOption
			// Options remains absent when the provider did not describe settings.
			Options json.RawMessage `json:"configOptions"`
		} `json:"models"`
		// DefaultID is only set when the provider identifies its default.
		DefaultID *acp.SessionConfigValueId `json:"defaultModelId"`
	}
	if json.Unmarshal(raw, &reply) != nil {
		return acpmeta.ModelCatalog{}, errors.New("invalid Cursor model catalog")
	}
	if reply.Models == nil {
		return acpmeta.ModelCatalog{}, acp.NewMethodNotFound(acpmeta.ModelCatalogMethod)
	}
	// Cursor 2026.10.22-27dcf5f returns an empty array on upstream
	// model fetch failure. It cannot prove an account's directory is empty.
	if len(reply.Models) == 0 {
		return acpmeta.ModelCatalog{}, errors.New("Cursor did not report an available model directory")
	}
	models := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(reply.Models))
	seen := make(map[acp.SessionConfigValueId]bool)
	for _, row := range reply.Models {
		if strings.TrimSpace(string(row.Value)) == "" || seen[row.Value] {
			return acpmeta.ModelCatalog{}, errors.New("invalid Cursor model catalog")
		}
		seen[row.Value] = true
		if len(row.Options) > 0 && string(row.Options) != "null" {
			options, err := acpclient.DecodeCatalogOptions(row.Options)
			if err != nil {
				return acpmeta.ModelCatalog{}, err
			}
			row.Meta = acpmeta.WithModelConfigOptions(row.Meta, options)
		}
		models = append(models, row.SessionConfigSelectOption)
	}
	catalog := acpmeta.ModelCatalog{Options: acp.SessionConfigSelectOptions{Ungrouped: &models}}
	if reply.DefaultID != nil && seen[*reply.DefaultID] {
		catalog.DefaultID = reply.DefaultID
	}
	return catalog, nil
}

// HandleExtensionMethod exposes Cursor management alongside the standard ACP projection.
func (c *Client) HandleExtensionMethod(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	switch method {
	case acpmeta.AccountReadMethod:
		return c.ReadAccount(ctx)
	case acpmeta.AccountUsageMethod:
		return c.ReadAccountUsage(ctx)
	default:
		return c.Client.HandleExtensionMethod(ctx, method, raw)
	}
}

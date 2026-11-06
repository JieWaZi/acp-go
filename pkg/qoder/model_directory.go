package qoder

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	acp "github.com/JieWaZi/acp-go-sdk"
	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"golang.org/x/sync/errgroup"
)

// ErrModelDirectoryUnsupported reports that the selected release has no SDK catalog query.
var ErrModelDirectoryUnsupported = errors.New("Qoder CLI SDK model directory is unsupported")

// ModelDirectoryEntry holds SDK model metadata and availability for the current account.
type ModelDirectoryEntry struct {
	// ID is the model identifier accepted by native selection.
	ID string `json:"value"`
	// Name is the display name supplied by Qoder.
	Name string `json:"displayName"`
	// Description is the model description supplied by Qoder.
	Description string `json:"description"`
	// New marks models assigned to the New group.
	New bool `json:"isNew"`
	// Enabled reports whether the current account can select the model.
	Enabled bool `json:"isEnabled"`
	// Free records the native catalog's free-model declaration.
	Free bool `json:"isFree"`
	// Source distinguishes native tiers from account-defined models.
	Source string `json:"source"`
	// Efforts lists reasoning levels advertised by the model.
	Efforts []string `json:"efforts"`
	// DefaultEffort is the default reasoning level reported by the SDK.
	DefaultEffort string `json:"defaultEffort"`
	// AvailableContextWindows lists selectable context sizes in tokens reported by the SDK.
	AvailableContextWindows []int64 `json:"availableContextWindows"`
	// DefaultContextWindow is the SDK's default context size in tokens.
	DefaultContextWindow int64 `json:"defaultContextWindow"`
	// PriceFactor is the credit multiplier; an explicit zero means no credit charge.
	PriceFactor *float64 `json:"priceFactor"`
}

// ReadModels merges SDK availability with ACP options while preserving ACP-confirmed settings.
func (c *Client) ReadModels(ctx context.Context) (acpmeta.ModelCatalog, error) {
	var selectable acpmeta.ModelCatalog
	var rows []ModelDirectoryEntry
	var acpErr, sdkErr error
	// ACP and SDK share the account lease but own separate processes and read operations.
	group, probeContext := errgroup.WithContext(ctx)
	group.Go(func() error {
		selectable, acpErr = c.Client.ReadModels(probeContext)
		var protocolErr *acp.RequestError
		if errors.As(acpErr, &protocolErr) && protocolErr.Code == -32601 {
			return nil
		}
		return acpErr
	})
	group.Go(func() error {
		rows, sdkErr = c.ReadModelDirectory(probeContext)
		if errors.Is(sdkErr, ErrModelDirectoryUnsupported) {
			return nil
		}
		return sdkErr
	})
	if err := group.Wait(); err != nil {
		return acpmeta.ModelCatalog{}, err
	}
	if sdkErr != nil {
		return mergeModelDirectory(selectable, nil), acpErr
	}
	return mergeModelDirectory(selectable, rows), nil
}

func mergeModelDirectory(selectable acpmeta.ModelCatalog, rows []ModelDirectoryEntry) acpmeta.ModelCatalog {
	native := make(map[acp.SessionConfigValueId]acp.SessionConfigSelectOption)
	visitNative := func(visit func(acp.SessionConfigSelectOption, acp.SessionConfigGroupId, string)) {
		if selectable.Options.Ungrouped != nil {
			for _, row := range *selectable.Options.Ungrouped {
				group, name := qoderACPModelGroup(row.Value)
				visit(row, group, name)
			}
		}
		if selectable.Options.Grouped != nil {
			for _, group := range *selectable.Options.Grouped {
				for _, row := range group.Options {
					visit(row, group.Group, group.Name)
				}
			}
		}
	}
	visitNative(func(row acp.SessionConfigSelectOption, _ acp.SessionConfigGroupId, _ string) {
		native[row.Value] = row
	})
	available := acp.SessionConfigSelectOptionsGrouped{}
	appendRow := func(groups *acp.SessionConfigSelectOptionsGrouped, row acp.SessionConfigSelectOption, group acp.SessionConfigGroupId, name string) {
		i := slices.IndexFunc(*groups, func(item acp.SessionConfigSelectGroup) bool { return item.Group == group })
		if i < 0 {
			*groups = append(*groups, acp.SessionConfigSelectGroup{Group: group, Name: name, Options: []acp.SessionConfigSelectOption{row}})
			return
		}
		(*groups)[i].Options = append((*groups)[i].Options, row)
	}
	result := acpmeta.ModelCatalog{Options: acp.SessionConfigSelectOptions{Grouped: &available}, Modes: selectable.Modes}
	addAvailable := func(row acp.SessionConfigSelectOption, group acp.SessionConfigGroupId, name string) {
		appendRow(&available, row, group, name)
		if selectable.DefaultID != nil && row.Value == *selectable.DefaultID {
			result.DefaultID = selectable.DefaultID
		}
	}
	for _, entry := range rows {
		id := acp.SessionConfigValueId(entry.ID)
		row, listed := native[id]
		// SDK account availability takes precedence over ACP options, including removal of disabled supplemental rows.
		delete(native, id)
		if !entry.Enabled {
			continue
		}
		if !listed {
			row = acp.SessionConfigSelectOption{Value: id, Name: entry.ID}
		}
		if entry.Name != "" {
			row.Name = entry.Name
		}
		if entry.Description != "" {
			row.Description = &entry.Description
		}
		if entry.Free {
			meta := make(map[string]any, len(row.Meta)+1)
			maps.Copy(meta, row.Meta)
			meta[acpmeta.ModelFreeMetaKey] = true
			row.Meta = meta
		}
		if entry.PriceFactor != nil {
			row.Meta = acpmeta.WithModelCreditMultiplier(row.Meta, *entry.PriceFactor)
		}
		row.Meta = withDirectoryReasoning(row.Meta, entry)
		row.Meta = withDirectoryContext(row.Meta, entry)
		group, name := acp.SessionConfigGroupId("qoder/default"), "Default"
		switch strings.ToLower(entry.Source) {
		case "custom", "byok", "user":
			group, name = "qoder/custom", "Custom"
		default:
			if entry.New {
				group, name = "qoder/new", "New"
			}
		}
		// New and custom models use the catalog union; the native session confirms whether the selection can run.
		if group != "qoder/default" || listed {
			addAvailable(row, group, name)
		}
	}
	// Preserve legacy ACP-only choices even when the SDK catalog omits them.
	visitNative(func(row acp.SessionConfigSelectOption, group acp.SessionConfigGroupId, name string) {
		if _, exists := native[row.Value]; exists {
			addAvailable(row, group, name)
			delete(native, row.Value)
		}
	})
	return result
}

func withDirectoryReasoning(meta map[string]any, entry ModelDirectoryEntry) map[string]any {
	options := acp.SessionConfigSelectOptionsUngrouped{}
	for _, value := range entry.Efforts {
		id := acp.SessionConfigValueId(value)
		if strings.TrimSpace(value) != "" && !slices.ContainsFunc(options, func(option acp.SessionConfigSelectOption) bool { return option.Value == id }) {
			options = append(options, acp.SessionConfigSelectOption{Value: id, Name: value})
		}
	}
	if len(options) == 0 {
		return meta
	}
	catalog, _ := acpmeta.ReadModelConfigCatalog(meta)
	isReasoning := func(option acp.SessionConfigOption) bool {
		return option.Select != nil && (option.Select.Id == "reasoning_effort" ||
			option.Select.Category != nil && *option.Select.Category == acp.SessionConfigOptionCategoryThoughtLevel)
	}
	if slices.ContainsFunc(catalog.Options, func(option acp.SessionConfigOption) bool {
		return isReasoning(option) && (option.Select.Options.Ungrouped != nil && len(*option.Select.Options.Ungrouped) != 0 ||
			option.Select.Options.Grouped != nil && len(*option.Select.Options.Grouped) != 0)
	}) {
		return meta
	}
	levels := []acp.SessionConfigValueId{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}
	rank := func(id acp.SessionConfigValueId) int {
		if index := slices.Index(levels, id); index >= 0 {
			return index
		}
		return len(levels)
	}
	slices.SortStableFunc(options, func(a, b acp.SessionConfigSelectOption) int {
		return rank(a.Value) - rank(b.Value)
	})
	var current acp.SessionConfigValueId
	if slices.ContainsFunc(options, func(option acp.SessionConfigSelectOption) bool { return string(option.Value) == entry.DefaultEffort }) {
		current = acp.SessionConfigValueId(entry.DefaultEffort)
	}
	catalog.Options = append(slices.DeleteFunc(catalog.Options, isReasoning), acp.SessionConfigOption{
		Select: &acp.SessionConfigOptionSelect{
			Id: "reasoning_effort", Name: "Reasoning effort", Type: "select",
			Category: acp.Ptr(acp.SessionConfigOptionCategoryThoughtLevel), CurrentValue: current,
			Options: acp.SessionConfigSelectOptions{Ungrouped: &options},
		},
	})
	catalog.UnknownConfigIDs = slices.DeleteFunc(catalog.UnknownConfigIDs, func(id acp.SessionConfigId) bool {
		switch id {
		case "reasoning", "reasoning_effort", "thinking_level", "thought_level":
			return true
		}
		return false
	})
	return acpmeta.WithModelConfigCatalog(meta, catalog)
}

func withDirectoryContext(meta map[string]any, entry ModelDirectoryEntry) map[string]any {
	options := acp.SessionConfigSelectOptionsUngrouped{}
	var current acp.SessionConfigValueId
	for _, size := range entry.AvailableContextWindows {
		id := acp.SessionConfigValueId(strconv.FormatInt(size, 10))
		if size <= 0 || slices.ContainsFunc(options, func(option acp.SessionConfigSelectOption) bool { return option.Value == id }) {
			continue
		}
		options = append(options, acp.SessionConfigSelectOption{Value: id, Name: string(id)})
		if size == entry.DefaultContextWindow {
			current = id
		}
	}
	if len(options) == 0 {
		return meta
	}
	const id = "context_window"
	catalog, _ := acpmeta.ReadModelConfigCatalog(meta)
	catalog.Options = append(slices.DeleteFunc(catalog.Options, func(option acp.SessionConfigOption) bool {
		return option.Select != nil && option.Select.Id == id
	}), acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id: id, Name: "Context window", Type: "select", Category: acp.Ptr(acp.SessionConfigOptionCategory(id)),
		CurrentValue: current, Options: acp.SessionConfigSelectOptions{Ungrouped: &options},
	}})
	catalog.UnknownConfigIDs = slices.DeleteFunc(catalog.UnknownConfigIDs, func(optionID acp.SessionConfigId) bool { return optionID == id })
	return acpmeta.WithModelConfigCatalog(meta, catalog)
}

func qoderACPModelGroup(id acp.SessionConfigValueId) (acp.SessionConfigGroupId, string) {
	switch strings.ToLower(string(id)) {
	case "auto", "ultimate", "performance", "fast", "efficient", "lite":
		return "qoder/default", "Default"
	default:
		return "qoder/new", "New"
	}
}

// ReadModelDirectory reads the complete SDK catalog without opening a session or submitting input.
func (c *Client) ReadModelDirectory(ctx context.Context) ([]ModelDirectoryEntry, error) {
	var result struct {
		// Models preserves native order and distinguishes an empty catalog from a missing field.
		Models []ModelDirectoryEntry `json:"models"`
	}
	err := c.management.daemonQueries(ctx, func(channel *controlChannel) error {
		if _, err := c.management.daemonIdentity(channel); err != nil {
			return err
		}
		if err := channel.requestPayload("daemon.get_models", map[string]string{
			"type": "daemon.get_models", "fetchStrategy": "live",
		}, &result); err != nil {
			return err
		}
		if result.Models == nil {
			return errors.New("Qoder CLI SDK model directory is missing")
		}
		seen := make(map[string]bool, len(result.Models))
		for _, row := range result.Models {
			if strings.TrimSpace(row.ID) == "" || seen[row.ID] {
				return errors.New("Qoder CLI SDK model directory contains an invalid model identifier")
			}
			seen[row.ID] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result.Models, nil
}

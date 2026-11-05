package qoder

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	acp "github.com/JieWaZi/acp-go/sdk"
)

type quotaReply struct {
	// Usage is null when the provider can only report session usage.
	Usage json.RawMessage `json:"usage"`
}

type quotaSnapshot struct {
	// UserID associates these quotas with the provider's account.
	UserID string `json:"userId"`
	// Plan is the allowance included in the current subscription.
	Plan *quotaBucket `json:"userQuota"`
	// AddOn is the optional purchased allowance.
	AddOn *quotaBucket `json:"addOnQuota"`
	// Organization is an independently metered shared resource package.
	Organization *quotaBucket `json:"orgResourcePackage"`
	// TotalPercent is an optional provider-wide percentage.
	TotalPercent *float64 `json:"totalUsagePercentage"`
}

type quotaBucket struct {
	// Total is the native plan or add-on limit.
	Total *json.Number `json:"total"`
	// Cap is the native organization limit.
	Cap *json.Number `json:"cap"`
	// Used reports the exact debit in the native unit.
	Used *json.Number `json:"used"`
	// Remaining is the reported balance in the native unit.
	Remaining *json.Number `json:"remaining"`
	// Percentage is the used percentage.
	Percentage *float64 `json:"percentage"`
	// Unit identifies the native resource unit.
	Unit *string `json:"unit"`
}

// ReadAccountUsage reads only account quotas on the selected credential.
func (c *Client) ReadAccountUsage(ctx context.Context) (acpmeta.AccountUsage, error) {
	var usage acpmeta.AccountUsage
	m := &c.management
	err := m.control(ctx, func(channel *controlChannel, bootstrap controlAccount) error {
		account, err := readControlIdentity(channel, bootstrap)
		if err != nil {
			return err
		}
		if err := m.verifyIdentity(account.UserID); err != nil {
			return err
		}
		var reply quotaReply
		if err := channel.request("get_usage_info", &reply); err != nil {
			return err
		}
		if string(reply.Usage) == "null" {
			usage.Available = acp.Ptr(false)
			return nil
		}
		var native quotaSnapshot
		if len(reply.Usage) == 0 || json.Unmarshal(reply.Usage, &native) != nil {
			return errors.New("Qoder CLI quota response is invalid")
		}
		if native.UserID != "" && account.UserID != "" && native.UserID != account.UserID {
			return errors.New("Qoder CLI quota does not match the selected subject")
		}
		if account.UserID == "" && selectedToken(m.options) == "" {
			return errors.New("Qoder CLI quota has no verified account identity")
		}
		usage = native.project()
		return nil
	})
	return usage, err
}

func (q quotaSnapshot) project() acpmeta.AccountUsage {
	var usage acpmeta.AccountUsage
	for _, row := range []struct {
		// ID is the native quota field.
		ID string
		// Bucket holds the exact reported dimensions.
		Bucket *quotaBucket
	}{
		{"userQuota", q.Plan}, {"addOnQuota", q.AddOn}, {"orgResourcePackage", q.Organization},
	} {
		if row.Bucket == nil {
			continue
		}
		native := row.Bucket
		bucket := acpmeta.AccountUsageBucket{ID: row.ID}
		limit := native.Total
		if row.ID == "orgResourcePackage" {
			limit = native.Cap
		}
		if native.Used != nil || limit != nil || native.Unit != nil {
			bucket.Extra = &acpmeta.AccountUsageExtra{Used: native.Used, Limit: limit, Unit: native.Unit}
		}
		if native.Remaining != nil {
			bucket.Balance = acp.Ptr(string(*native.Remaining))
		}
		if native.Percentage != nil {
			bucket.Windows = []acpmeta.AccountUsageWindow{{Kind: "primary", UsedPercent: native.Percentage}}
		}
		if bucket.Extra != nil || bucket.Balance != nil || len(bucket.Windows) > 0 {
			usage.Buckets = append(usage.Buckets, bucket)
		}
	}
	if len(usage.Buckets) == 0 && q.TotalPercent != nil {
		usage.Buckets = []acpmeta.AccountUsageBucket{{ID: "totalUsagePercentage", Windows: []acpmeta.AccountUsageWindow{
			{Kind: "primary", UsedPercent: q.TotalPercent},
		}}}
	}
	usage.Available = acp.Ptr(len(usage.Buckets) > 0)
	return usage
}

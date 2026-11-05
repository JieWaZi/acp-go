package qoder

import (
	"encoding/json"
	"strings"

	"github.com/JieWaZi/acp-go/pkg/acpmeta"
	"github.com/JieWaZi/acp-go/pkg/agent_next/acpclient"
)

const maxNativeTokenCounter = 1<<53 - 1

type qoderPromptTokenCounts struct {
	// Input is the native prompt input, with potentially overlapping cache tokens.
	Input *int64 `json:"input_tokens"`
	// Output is the measured generated-token subtotal.
	Output *int64 `json:"output_tokens"`
}

type qoderPromptModelUsage struct {
	// Model is the native model identity from this query's model ledger.
	Model string `json:"model"`
	// Counts holds the reported token counters for the model.
	Counts qoderPromptTokenCounts `json:"token_count"`
}

type qoderPromptQuota struct {
	// Counts repeats the aggregate from this ACP prompt's query pipeline.
	Counts qoderPromptTokenCounts `json:"token_count"`
	// Models describes the same invocation broken down by model.
	Models []qoderPromptModelUsage `json:"model_usage"`
}

// readPromptUsage follows the Qoder 1.1.53 ACP promptViaQueryEngine receipt.
// Its SDK result is built from the current agent loop's usage, before the
// separate session-cumulative ledger. Both counters can be redacted to zero,
// and this wire shape omits cache and reasoning. Keep the positive output
// subtotal and the separately observable, potentially cache-inclusive input.
func readPromptUsage(version string, raw *acpclient.PromptUsageCounters, meta map[string]any) (*acpmeta.TurnUsage, acpclient.PromptUsageScope) {
	if version != "1.1.53" || raw == nil || raw.CacheRead != nil || raw.CacheWrite != nil || raw.Thought != nil ||
		!validQoderTokenCount(raw.Input) ||
		!validQoderTokenCount(raw.Output) || !validQoderTokenCount(raw.Total) ||
		*raw.Total != *raw.Input+*raw.Output || *raw.Total == 0 {
		return nil, acpclient.UsageUnattributed
	}
	var quota qoderPromptQuota
	body, err := json.Marshal(meta["quota"])
	if err != nil || len(body) > 1<<20 || json.Unmarshal(body, &quota) != nil ||
		!validQoderTokenCount(quota.Counts.Input) || !validQoderTokenCount(quota.Counts.Output) ||
		*quota.Counts.Input != *raw.Input || *quota.Counts.Output != *raw.Output {
		return nil, acpclient.UsageUnattributed
	}
	const source = "qoder.prompt.quota"
	report := &acpmeta.TurnUsage{Source: source, Coverage: "query_pipeline"}
	if *raw.Output > 0 {
		report.OutputTokens = raw.Output
		report.Models = qoderOutputByModel(quota.Models, *raw.Output)
	}
	report.Observations = []acpmeta.UsageObservation{{
		Source: source, Coverage: "query_pipeline_raw", TotalTokens: raw.Total,
	}}
	if *raw.Input > 0 {
		report.Observations[0].InputTotalTokens = raw.Input
	}
	return report, acpclient.UsageUnavailable
}

func qoderOutputByModel(rows []qoderPromptModelUsage, output int64) map[string]acpmeta.ModelUsage {
	if len(rows) == 0 || len(rows) > 128 {
		return nil
	}
	models := make(map[string]acpmeta.ModelUsage, len(rows))
	for _, row := range rows {
		if !validQoderTokenCount(row.Counts.Output) {
			return nil
		}
		if *row.Counts.Output == 0 {
			// Native model rows can include historical models with no current tokens.
			continue
		}
		if row.Model == "" || row.Model == "unknown" || strings.TrimSpace(row.Model) != row.Model ||
			*row.Counts.Output > output {
			return nil
		}
		if _, duplicate := models[row.Model]; duplicate {
			return nil
		}
		output -= *row.Counts.Output
		models[row.Model] = acpmeta.ModelUsage{OutputTokens: row.Counts.Output}
	}
	if output != 0 {
		return nil
	}
	return models
}

func validQoderTokenCount(count *int64) bool {
	return count != nil && *count >= 0 && *count <= maxNativeTokenCounter
}

func tokenUsageEnvironment(runtimeID string) string {
	if runtimeID == "qoder-cn" {
		return "QODERCN_EXPOSE_TOKEN_USAGE=1"
	}
	return "QODER_EXPOSE_TOKEN_USAGE=1"
}

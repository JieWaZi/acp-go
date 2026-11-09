package qoder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestQoderHistoryAutoUsesBoundNativeRuntimeModel(t *testing.T) {
	for _, scenario := range []string{"selected", "explicit", "missing", "foreign", "future", "conflict", "automatic", "latest", "duplicate-config", "foreign-cwd", "custom"} {
		t.Run(scenario, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			cwd, _ = filepath.EvalSymlinks(cwd)
			session, turn := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
			model, want := "auto", "dfmodel"
			if scenario == "explicit" {
				model, want = "actual-model", "actual-model"
			}
			if scenario != "selected" && scenario != "explicit" && scenario != "latest" && scenario != "duplicate-config" {
				want = "auto"
			}
			config := map[string]any{"type": "runtime-config", "sessionId": session, "model": "dfmodel", "timestamp": 1000}
			if scenario == "foreign" {
				config["sessionId"] = "another-session"
			}
			if scenario == "future" {
				config["timestamp"] = 3000
			}
			if scenario == "automatic" {
				config["model"] = "auto"
			}
			rows := []map[string]any{
				{"type": "user", "uuid": turn, "sessionId": session, "cwd": cwd, "timestamp": "1970-01-01T00:00:02Z"},
				{"type": "assistant", "uuid": "response", "parentUuid": turn, "sessionId": session, "cwd": cwd, "message": map[string]any{"id": "response-id", "model": model, "usage": map[string]any{"input_tokens": 20, "output_tokens": 5, "request_id": "request-id"}}},
			}
			if scenario == "foreign-cwd" {
				config["cwd"] = "/other-workspace"
			}
			if scenario == "custom" {
				rows[1]["modelSource"] = "custom"
			}
			if scenario == "latest" {
				rows = append(rows, map[string]any{"type": "runtime-config", "sessionId": session, "model": "old-model", "timestamp": 500})
			}
			if scenario == "duplicate-config" {
				rows = append(rows, config)
			}
			if scenario != "missing" {
				rows = append(rows, config)
			}
			if scenario == "conflict" {
				rows = append(rows, map[string]any{"type": "runtime-config", "sessionId": session, "model": "another-model", "timestamp": 1000})
			}
			file, err := os.Create(filepath.Join(home, "history.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if err := json.NewEncoder(file).Encode(row); err != nil {
					t.Fatal(err)
				}
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			usage := readQoderHistory(t.Context(), home, "history.jsonl", cwd, session, turn, nil)
			if usage == nil || usage.Model != want || usage.Models[want].OutputTokens == nil || *usage.Models[want].OutputTokens != 5 {
				t.Fatalf("model attribution: want %s, got %+v", want, usage)
			}
		})
	}
}

package cursor

import "testing"

// TestMissingParameterizedCatalog 防止把普通模型不可用、认证或配额错误误判为可自动重启的目录缺失。
func TestMissingParameterizedCatalog(t *testing.T) {
	model := "composer-2.5[fast=true]"
	for _, screen := range []string{
		"Cannot use this model: composer-2.5[fast=true]. Available models: auto, composer-2.5-fast",
		"Cannot use this model: composer-2.5[fast=true].\nAvailable models: auto",
	} {
		if !missingParameterizedCatalog(screen, model) {
			t.Fatalf("catalog failure not recognized: %s", screen)
		}
	}
	for _, screen := range []string{
		"Upgrade your plan",
		"Cannot use this model: grok-4.6[effort=high]. Available models: auto",
		"Cannot use this model: composer-2.5[fast=true]. Bedrock disabled",
		"Cannot use this model: composer-2.5. Available models: auto",
	} {
		if missingParameterizedCatalog(screen, model) {
			t.Fatalf("unrelated failure retried: %s", screen)
		}
	}
}

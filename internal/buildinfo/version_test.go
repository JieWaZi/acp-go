package buildinfo

import "testing"

// TestCurrentPrefersInjectedVersion 验证发布流水线值优先于 Go module build info。
func TestCurrentPrefersInjectedVersion(t *testing.T) {
	original := Version
	Version = "v1.2.3"
	t.Cleanup(func() { Version = original })
	if got := Current(); got != "v1.2.3" {
		t.Fatalf("Current() = %q", got)
	}
}

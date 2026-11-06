package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSystemSubscriptionIdentityUsesNativePlatformHome(t *testing.T) {
	const (
		org            = "c58fe389-3a47-4d19-aa06-104f4450dff8"
		email          = "local@example.test"
		posixAccount   = "5e672c97-89c8-466f-8f0e-b2dc761c587b"
		windowsAccount = "d84d178e-3113-4fb3-a03c-97eeb2b194c2"
	)
	home, profile := t.TempDir(), t.TempDir()
	for dir, account := range map[string]string{home: posixAccount, profile: windowsAccount} {
		metadata := fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q,"organizationUuid":%q,"emailAddress":%q}}`, account, org, email)
		if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(metadata), 0600); err != nil {
			t.Fatal(err)
		}
	}
	client := &Client{options: Options{Environment: []string{"HOME=" + home, "USERPROFILE=" + profile}}}
	for platform, want := range map[string]string{"darwin": posixAccount, "linux": posixAccount, "windows": windowsAccount} {
		if account := client.subscriptionAccountID(org, email, platform); account != want {
			t.Errorf("%s selected account %q, want %q", platform, account, want)
		}
	}
}

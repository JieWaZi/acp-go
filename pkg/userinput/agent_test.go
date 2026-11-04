package userinput

import (
	"testing"

	acp "github.com/JieWaZi/acp-go/sdk"
)

// sessionOwnerAgent controls whether the wrapped adapter owns native session cancellation.
type sessionOwnerAgent struct {
	// Agent provides the untouched ACP protocol surface.
	acp.Agent
	// owns is the opt-in reported by the underlying adapter.
	owns bool
}

// ManagesSessionLifecycle returns the adapter's marker.
func (a sessionOwnerAgent) ManagesSessionLifecycle() bool { return a.owns }

// TestWrapPreservesSessionLifecycleOwnership tests the actual wrapper used by acpserver.
func TestWrapPreservesSessionLifecycleOwnership(t *testing.T) {
	tests := []struct {
		// agent is the adapter installed by the host.
		agent acp.Agent
		// owns is the ownership flag visible after wrapping.
		owns bool
	}{
		{nil, false},
		{sessionOwnerAgent{}, false},
		{sessionOwnerAgent{owns: true}, true},
	}
	for _, tt := range tests {
		var wrapped any = Wrap(tt.agent)
		owner, supported := wrapped.(interface {
			// ManagesSessionLifecycle reports the wrapped adapter's marker.
			ManagesSessionLifecycle() bool
		})
		if !supported || owner.ManagesSessionLifecycle() != tt.owns {
			t.Fatalf("wrapper of %T changed ownership: supported=%v, want owns=%v", tt.agent, supported, tt.owns)
		}
	}
}

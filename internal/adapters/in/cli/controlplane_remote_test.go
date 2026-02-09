package cli

import (
	"testing"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

var _ ControlPlane = (*remoteControlPlane)(nil)

func TestRemoteControlPlane_ImplementsInterface(t *testing.T) {
	client := remote.NewClient("https://gordon.example.com")
	if NewRemoteControlPlane(client) == nil {
		t.Fatal("expected non-nil remote control-plane")
	}
}

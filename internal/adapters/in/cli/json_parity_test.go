package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestAllListCommands_AcceptJSONFlag(t *testing.T) {
	commands := []struct {
		name    string
		builder func() *cobra.Command
	}{
		{"secrets list", newSecretsListCmd},
		{"images list", newImagesListCmd},
		{"backup list", newBackupListCmd},
		{"backup volumes list", newVolumeBackupListCmd},
		{"backup volumes run", newVolumeBackupRunCmd},
		{"backup volumes status", newVolumeBackupStatusCmd},
		{"auth token list", newTokenListCmd},
		{"remotes list", newRemotesListCmd},
		{"config show", newConfigShowCmd},
		{"networks list", newNetworksListCmd},
		{"images tags", newImagesTagsCmd},
		{"tls status", newTLSStatusCmd},
	}

	for _, tc := range commands {
		t.Run(tc.name, func(t *testing.T) {
			cmd := tc.builder()
			f := cmd.Flags().Lookup("json")
			assert.NotNil(t, f, "command %q should have --json flag", tc.name)
		})
	}
}

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
		{"routes list", newRoutesListCmd},
		{"routes status", newRoutesStatusCmd},
		{"attachments list", newAttachmentsListCmd},
		{"secrets list", newSecretsListCmd},
		{"images list", newImagesListCmd},
		{"backup list", newBackupListCmd},
		{"auth token list", newTokenListCmd},
		{"remotes list", newRemotesListCmd},
		{"pin list", newPinListCmd},
		{"config show", newConfigShowCmd},
		{"routes show", newRoutesShowCmd},
		{"networks list", newNetworksListCmd},
		{"images tags", newImagesTagsCmd},
	}

	for _, tc := range commands {
		t.Run(tc.name, func(t *testing.T) {
			cmd := tc.builder()
			f := cmd.Flags().Lookup("json")
			assert.NotNil(t, f, "command %q should have --json flag", tc.name)
		})
	}
}

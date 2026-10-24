package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestRootRuntimeErrorOmitsUsage(t *testing.T) {
	root := NewRootCmd()
	root.AddCommand(&cobra.Command{
		Use:  "runtime-failure",
		RunE: func(*cobra.Command, []string) error { return errors.New("server rejected request") },
	})
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"runtime-failure"})
	require.EqualError(t, root.Execute(), "server rejected request")
	require.Contains(t, output.String(), "Error: server rejected request")
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte("Error:")))
	require.NotContains(t, output.String(), "Usage:")
}

func TestRootUnknownFlagHasHint(t *testing.T) {
	root := NewRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"apps", "status", "--not-a-flag"})
	require.Error(t, root.Execute())
	require.Contains(t, output.String(), "unknown flag")
	require.Contains(t, output.String(), "Run 'gordon apps status --help' for usage.")
	require.NotContains(t, output.String(), "Flags:")
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte("Error:")))
}

func TestRootUsageErrorsRemainHelpful(t *testing.T) {
	for _, args := range [][]string{{"not-a-command"}, {"apps", "status"}} {
		t.Run(args[0], func(t *testing.T) {
			root := NewRootCmd()
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&output)
			root.SetArgs(args)
			require.Error(t, root.Execute())
			require.Contains(t, output.String(), "Error:")
			require.NotContains(t, output.String(), "Usage:")
		})
	}
}

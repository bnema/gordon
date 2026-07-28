package compatoldnew

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRepositoryRootResolvesWithoutTestingT(t *testing.T) {
	root := RepositoryRoot()
	require.DirExists(t, root)
	_, err := os.Stat(filepath.Join(root, "go.mod"))
	require.NoError(t, err)
	require.Equal(t, root, projectRoot(t))
}

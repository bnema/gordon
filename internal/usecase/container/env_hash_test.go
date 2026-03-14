package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashEnvironment_Deterministic(t *testing.T) {
	env1 := []string{"B=2", "A=1", "C=3"}
	env2 := []string{"A=1", "C=3", "B=2"}
	assert.Equal(t, hashEnvironment(env1), hashEnvironment(env2))
}

func TestHashEnvironment_DifferentValues(t *testing.T) {
	env1 := []string{"A=1", "B=2"}
	env2 := []string{"A=1", "B=3"}
	assert.NotEqual(t, hashEnvironment(env1), hashEnvironment(env2))
}

func TestHashEnvironment_DifferentKeys(t *testing.T) {
	env1 := []string{"A=1", "B=2"}
	env2 := []string{"A=1", "C=2"}
	assert.NotEqual(t, hashEnvironment(env1), hashEnvironment(env2))
}

func TestHashEnvironment_Empty(t *testing.T) {
	hash := hashEnvironment(nil)
	require.NotEmpty(t, hash)
	assert.Equal(t, hash, hashEnvironment([]string{}))
}

func TestHashEnvironment_ExtraKey(t *testing.T) {
	env1 := []string{"A=1"}
	env2 := []string{"A=1", "B=2"}
	assert.NotEqual(t, hashEnvironment(env1), hashEnvironment(env2))
}

func TestHashEnvironment_NewlineInValue(t *testing.T) {
	env1 := []string{"A=1\nB=2"}
	env2 := []string{"A=1", "B=2"}

	assert.NotEqual(t, hashEnvironment(env1), hashEnvironment(env2))
}

func TestMergeEnvironmentVariables_Sorted(t *testing.T) {
	dockerfile := []string{"Z=26", "A=1"}
	user := []string{"M=13", "B=2"}
	result := mergeEnvironmentVariables(dockerfile, user)
	assert.Equal(t, []string{"A=1", "B=2", "M=13", "Z=26"}, result)
}

func TestMergeEnvironmentVariables_UserOverridesDockerfile(t *testing.T) {
	dockerfile := []string{"A=old"}
	user := []string{"A=new"}
	result := mergeEnvironmentVariables(dockerfile, user)
	assert.Equal(t, []string{"A=new"}, result)
}

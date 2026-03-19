package preview

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildCloneVolumeName(t *testing.T) {
	assert.Equal(t, "preview-feat-pgdata", BuildCloneVolumeName("feat", "pgdata"))
	assert.Equal(t, "preview-pr-42-redis-data", BuildCloneVolumeName("pr-42", "redis-data"))
}

func TestBuildCloneContainerName(t *testing.T) {
	assert.Equal(t, "preview-feat-postgres", BuildCloneContainerName("feat", "postgres:17"))
	assert.Equal(t, "preview-feat-redis", BuildCloneContainerName("feat", "redis:7"))
	assert.Equal(t, "preview-feat-myapp", BuildCloneContainerName("feat", "org/myapp:latest"))
}

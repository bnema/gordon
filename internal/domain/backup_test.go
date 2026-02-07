package domain

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDBInfoJSONOmitsCredentials(t *testing.T) {
	info := DBInfo{
		Type: DBTypePostgreSQL,
		Name: "postgres",
		Credentials: map[string]string{
			"password": "secret",
		},
	}

	payload, err := json.Marshal(info)
	require.NoError(t, err)
	assert.NotContains(t, string(payload), "credentials")
	assert.NotContains(t, string(payload), "secret")
}

package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestValidateReadinessPath(t *testing.T) {
	valid := []string{"/", "/healthz", "/healthz?probe=1", "/a/b-c_d.e"}
	for _, path := range valid {
		require.NoError(t, domain.ValidateReadinessPath(path), "path %q should be accepted", path)
	}

	invalid := []string{
		"",
		"   ",
		"healthz",
		"@127.0.0.1:9/internal",
		"//evil.example.com/path",
		"http://evil.example.com/path",
		"https://evil.example.com/path",
		"/healthz\r\nHost: evil",
		"/healthz\x00",
		"/healthz\x7f",
		"/he\talthz",
	}
	for _, path := range invalid {
		assert.Error(t, domain.ValidateReadinessPath(path), "path %q should be rejected", path)
	}
}

func TestAppSpecValidate_RejectsAuthorityInReadinessPath(t *testing.T) {
	spec := domain.AppSpec{
		Name: "blog",
		Services: []domain.AppService{{
			Name:      "web",
			Image:     "img:1",
			Readiness: domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "@127.0.0.1:9/internal", Timeout: 30},
		}},
	}
	assert.ErrorIs(t, spec.Validate(), domain.ErrInvalidAppSpec)
}

package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func fileSecretSpec() domain.AppSpec {
	spec := validSpec()
	spec.Services[0].SecretFiles = []string{"DATABASE_URL"}
	return spec
}

func TestAppSpec_ValidateSecretFilesOK(t *testing.T) {
	require.NoError(t, fileSecretSpec().Validate())
	assert.True(t, fileSecretSpec().Services[0].SecretAsFile("DATABASE_URL"))
	assert.False(t, validSpec().Services[0].SecretAsFile("DATABASE_URL"))
	assert.Equal(t, "DATABASE_URL_FILE", domain.SecretFileEnvKey("DATABASE_URL"))
	assert.Equal(t, "/run/secrets/DATABASE_URL", domain.SecretFilePath("DATABASE_URL"))
}

func TestAppSpec_ValidateSecretFilesErrors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*domain.AppSpec)
		wantErr string
	}{
		{"not a secret", func(s *domain.AppSpec) { s.Services[0].SecretFiles = []string{"OTHER"} }, "not a declared secret"},
		{"unsorted", func(s *domain.AppSpec) {
			s.Services[0].Secrets["API"] = "api"
			s.Services[0].SecretFiles = []string{"DATABASE_URL", "API"}
		}, "must be sorted"},
		{"duplicate", func(s *domain.AppSpec) {
			s.Services[0].SecretFiles = []string{"DATABASE_URL", "DATABASE_URL"}
		}, "duplicated"},
		{"file key collides with secret", func(s *domain.AppSpec) {
			s.Services[0].Secrets["DATABASE_URL_FILE"] = "other"
		}, "collides with a secret file key"},
		{"file key collides with service env", func(s *domain.AppSpec) {
			s.Services[0].Env = map[string]string{"DATABASE_URL_FILE": "x"}
		}, "collides with a secret file key"},
		{"file key collides with app env", func(s *domain.AppSpec) {
			s.Env["DATABASE_URL_FILE"] = "x"
		}, "collides with a secret file key"},
		{"bind at secret dir", func(s *domain.AppSpec) {
			s.Services[0].Binds = []domain.AppBind{{Name: "conf", Path: "/run/secrets", ReadOnly: true}}
		}, "/run/secrets"},
		{"bind under secret dir", func(s *domain.AppSpec) {
			s.Services[0].Binds = []domain.AppBind{{Name: "conf", Path: "/run/secrets/x", ReadOnly: true}}
		}, "/run/secrets"},
		{"volume under secret dir", func(s *domain.AppSpec) {
			s.Services[0].Volumes = []domain.AppVolume{{Name: "data", Path: "/run/secrets/data"}}
		}, "/run/secrets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := fileSecretSpec()
			tc.mutate(&spec)
			err := spec.Validate()
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidAppSpec)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestAppSpec_SecretDirBindWithoutFileSecretsOK(t *testing.T) {
	spec := validSpec()
	spec.Services[0].Binds = []domain.AppBind{{Name: "conf", Path: "/run/secrets", ReadOnly: true}}
	require.NoError(t, spec.Validate())
}

func TestDiffAppSpec_SecretFileModeSwitch(t *testing.T) {
	envSpec := validSpec()
	fileSpec := fileSecretSpec()

	diff := domain.DiffAppSpec(fileSpec, envSpec)
	assert.Equal(t, []string{"service/web/secret/database-url"}, diff.Changed)
	assert.False(t, domain.SameAppService(fileSpec.Services[0], envSpec.Services[0]))

	back := domain.DiffAppSpec(envSpec, fileSpec)
	assert.Equal(t, []string{"service/web/secret/database-url"}, back.Changed)

	same := domain.DiffAppSpec(fileSpec, fileSecretSpec())
	assert.Empty(t, same.Changed)
	assert.True(t, domain.SameAppService(fileSpec.Services[0], fileSecretSpec().Services[0]))

	old := validSpec()
	require.Nil(t, old.Services[0].SecretFiles)
	assert.Empty(t, domain.DiffAppSpec(old, validSpec()).Changed)
	assert.True(t, domain.SameAppService(old.Services[0], validSpec().Services[0]))
}

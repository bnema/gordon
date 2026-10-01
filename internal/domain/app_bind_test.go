package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestAppSpec_ValidateBindsOK(t *testing.T) {
	spec := validSpec()
	spec.Services[0].Binds = []domain.AppBind{
		{Name: "config", Path: "/etc/app.conf", ReadOnly: true},
		{Name: "data", Path: "/var/lib/app"},
	}
	require.NoError(t, spec.Validate())
}

func TestAppSpec_ValidateBindErrors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*domain.AppSpec)
		wantErr string
	}{
		{"bad bind name", func(s *domain.AppSpec) {
			s.Services[0].Binds = []domain.AppBind{{Name: "Bad_Name", Path: "/data"}}
		}, "bind name"},
		{"reserved separator name", func(s *domain.AppSpec) {
			s.Services[0].Binds = []domain.AppBind{{Name: "a--b", Path: "/data"}}
		}, "reserved separator"},
		{"relative destination", func(s *domain.AppSpec) {
			s.Services[0].Binds = []domain.AppBind{{Name: "data", Path: "relative/path"}}
		}, "must be absolute"},
		{"unnormalized destination", func(s *domain.AppSpec) {
			s.Services[0].Binds = []domain.AppBind{{Name: "data", Path: "/var/../data"}}
		}, "normalized clean path"},
		{"trailing slash destination", func(s *domain.AppSpec) {
			s.Services[0].Binds = []domain.AppBind{{Name: "data", Path: "/data/"}}
		}, "normalized clean path"},
		{"sensitive destination", func(s *domain.AppSpec) {
			s.Services[0].Binds = []domain.AppBind{{Name: "dev", Path: "/dev"}}
		}, "sensitive container path"},
		{"duplicate bind name", func(s *domain.AppSpec) {
			s.Services[0].Binds = []domain.AppBind{
				{Name: "data", Path: "/data"},
				{Name: "data", Path: "/other"},
			}
		}, "duplicate bind name"},
		{"duplicate bind destination", func(s *domain.AppSpec) {
			s.Services[0].Binds = []domain.AppBind{
				{Name: "one", Path: "/data"},
				{Name: "two", Path: "/data"},
			}
		}, "duplicate bind destination"},
		{"collision with volume", func(s *domain.AppSpec) {
			s.Services[0].Volumes = []domain.AppVolume{{Name: "data", Path: "/data"}}
			s.Services[0].Binds = []domain.AppBind{{Name: "data-bind", Path: "/data"}}
		}, "collides with a declared volume"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := validSpec()
			tc.mutate(&spec)
			err := spec.Validate()
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidAppSpec)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestValidateBindName(t *testing.T) {
	assert.NoError(t, domain.ValidateBindName("data"))
	assert.NoError(t, domain.ValidateBindName("app.config"))
	assert.ErrorIs(t, domain.ValidateBindName(""), domain.ErrInvalidAppSpec)
	assert.ErrorIs(t, domain.ValidateBindName("Bad"), domain.ErrInvalidAppSpec)
	assert.ErrorIs(t, domain.ValidateBindName("a--b"), domain.ErrInvalidAppSpec)
}

func TestValidateBindDestination(t *testing.T) {
	valid := []string{"/data", "/etc/app.conf", "/var/lib/app"}
	for _, dest := range valid {
		assert.NoError(t, domain.ValidateBindDestination(dest), dest)
	}
	invalid := []string{"", "data", "/data/", "/data/../etc", "//data", "/./data", "/", "/proc", "/sys", "/dev", "/boot"}
	for _, dest := range invalid {
		err := domain.ValidateBindDestination(dest)
		require.Error(t, err, dest)
		assert.ErrorIs(t, err, domain.ErrInvalidAppSpec, dest)
	}
}

func TestIsSensitiveBindDestination(t *testing.T) {
	for _, dest := range []string{"/", "/proc", "/sys", "/dev", "/boot"} {
		assert.True(t, domain.IsSensitiveBindDestination(dest), dest)
	}
	assert.False(t, domain.IsSensitiveBindDestination("/etc/app.conf"))
	assert.True(t, domain.IsSensitiveBindDestination("/proc/self"))
	assert.True(t, domain.IsSensitiveBindDestination("/dev/shm"))
}

func TestDiffAppSpec_Binds(t *testing.T) {
	desired := validSpec()
	desired.Services[0].Binds = []domain.AppBind{{Name: "data", Path: "/data", ReadOnly: true}}
	effective := validSpec()
	effective.Services[0].Binds = []domain.AppBind{{Name: "data", Path: "/data"}}
	diff := domain.DiffAppSpec(desired, effective)
	assert.Contains(t, diff.Changed, "service/web/binds")
}

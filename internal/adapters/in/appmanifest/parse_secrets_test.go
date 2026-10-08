package appmanifest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/appmanifest"
	"github.com/bnema/gordon/internal/domain"
)

const secretsHead = "name = \"blog\"\n[services.web]\nimage = \"img:1\"\n"

func TestParse_SecretForms(t *testing.T) {
	cases := []struct {
		name      string
		doc       string
		wantMap   map[string]string
		wantFiles []string
	}{
		{"short form", "[services.web.secrets]\nDB = \"db\"\n", map[string]string{"DB": "db"}, nil},
		{"table name only", "[services.web.secrets]\nDB = { name = \"db\" }\n", map[string]string{"DB": "db"}, nil},
		{"table env", "[services.web.secrets]\nDB = { name = \"db\", as = \"env\" }\n", map[string]string{"DB": "db"}, nil},
		{"table file", "[services.web.secrets]\nDB = { name = \"db\", as = \"file\" }\n", map[string]string{"DB": "db"}, []string{"DB"}},
		{"sub-table", "[services.web.secrets.DB]\nname = \"db\"\nas = \"file\"\n", map[string]string{"DB": "db"}, []string{"DB"}},
		{
			"mixed",
			"[services.web.secrets]\nZ = { name = \"z\", as = \"file\" }\nA = { name = \"a\", as = \"file\" }\nM = \"m\"\nE = { name = \"e\", as = \"env\" }\n",
			map[string]string{"Z": "z", "A": "a", "M": "m", "E": "e"},
			[]string{"A", "Z"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, _, err := appmanifest.Parse([]byte(secretsHead+tc.doc), "blog.toml")
			require.NoError(t, err)
			require.Len(t, spec.Services, 1)
			assert.Equal(t, tc.wantMap, spec.Services[0].Secrets)
			assert.Equal(t, tc.wantFiles, spec.Services[0].SecretFiles)
		})
	}
}

func TestParse_NoSecretsKeepsEmptyMap(t *testing.T) {
	spec, _, err := appmanifest.Parse([]byte(secretsHead), "blog.toml")
	require.NoError(t, err)
	assert.NotNil(t, spec.Services[0].Secrets)
	assert.Empty(t, spec.Services[0].Secrets)
	assert.Nil(t, spec.Services[0].SecretFiles)
}

func TestParse_SecretFormsInvalid(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"bad as", "[services.web.secrets]\nDB = { name = \"db\", as = \"volume\" }\n", "as must be"},
		{"empty as", "[services.web.secrets]\nDB = { name = \"db\", as = \"\" }\n", "as must be"},
		{"as not string", "[services.web.secrets]\nDB = { name = \"db\", as = 1 }\n", "as must be"},
		{"missing name", "[services.web.secrets]\nDB = { as = \"file\" }\n", "name must be"},
		{"name not string", "[services.web.secrets]\nDB = { name = 1 }\n", "name must be"},
		{"extra key", "[services.web.secrets]\nDB = { name = \"db\", mode = \"x\" }\n", "unknown key \"mode\""},
		{"integer value", "[services.web.secrets]\nDB = 42\n", "secret name string"},
		{"array value", "[services.web.secrets]\nDB = [\"db\"]\n", "secret name string"},
		{"file key collision", "[services.web.env]\nDB_FILE = \"x\"\n[services.web.secrets]\nDB = { name = \"db\", as = \"file\" }\n", "collides with a secret file key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := appmanifest.Parse([]byte(secretsHead+tc.doc), "blog.toml")
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidAppSpec)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

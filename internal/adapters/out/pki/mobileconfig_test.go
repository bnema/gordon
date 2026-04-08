package pki_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/pki"
)

func TestGenerateMobileconfig(t *testing.T) {
	dir := t.TempDir()
	ca, err := pki.NewCA(dir, testLogger())
	require.NoError(t, err)

	mc := pki.GenerateMobileconfig(ca.RootCertificateDER(), ca.RootCommonName())
	assert.True(t, strings.HasPrefix(string(mc), "<?xml"))
	assert.Contains(t, string(mc), "Gordon Internal CA")
	assert.Contains(t, string(mc), "<key>PayloadType</key>")
	assert.Contains(t, string(mc), "com.apple.security.root")
}

func TestGenerateMobileconfig_UsesIdentifierSafePayloadIdentifier(t *testing.T) {
	mc := pki.GenerateMobileconfig([]byte("cert"), `Gordon Internal CA <prod>`)

	assert.Contains(t, string(mc), "<string>Gordon Internal CA &lt;prod&gt;</string>")
	assert.Contains(t, string(mc), "<string>dev.gordon.ca.gordon-internal-ca-prod</string>")
	assert.NotContains(t, string(mc), "dev.gordon.ca.Gordon Internal CA")
	assert.NotContains(t, string(mc), "dev.gordon.ca.gordon-internal-ca-&lt;prod&gt;")
}

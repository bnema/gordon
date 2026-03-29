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

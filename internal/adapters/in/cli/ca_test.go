package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/mocks"
	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
)

func testCertPEM(t *testing.T, isCA bool) (pemStr, fingerprint string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Remote Root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  isCA,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), pkiadapter.CertFingerprint(cert)
}

func testRootPEM(t *testing.T) string {
	t.Helper()
	p, _ := testCertPEM(t, true)
	return p
}

// stubCARemote swaps the package-level caResolveRemote. Tests using it must
// not run in parallel.
func stubCARemote(t *testing.T, cp ControlPlane) {
	t.Helper()
	prev := caResolveRemote
	caResolveRemote = func() (ControlPlane, error) { return cp, nil }
	t.Cleanup(func() { caResolveRemote = prev })
}

func TestCAExportRemoteUsesControlPlane(t *testing.T) {
	rootPEM := testRootPEM(t)
	cp := mocks.NewMockControlPlane(t)
	cp.EXPECT().GetCA(context.Background()).Return(&dto.CAResponse{RootPEM: rootPEM, RootCN: "Remote Root"}, nil).Once()
	stubCARemote(t, cp)

	cmd := newCAExportCmd()
	var buf, errBuf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&errBuf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))

	assert.Equal(t, rootPEM, buf.String(), "stdout carries only the PEM")
	assert.Contains(t, errBuf.String(), "fingerprint", "stderr asks to verify the fingerprint out of band")
	assert.Contains(t, errBuf.String(), "gordon ca info")
}

func TestCAExportRemoteWritesFile(t *testing.T) {
	rootPEM := testRootPEM(t)
	cp := mocks.NewMockControlPlane(t)
	cp.EXPECT().GetCA(context.Background()).Return(&dto.CAResponse{RootPEM: rootPEM}, nil).Once()
	stubCARemote(t, cp)

	outPath := filepath.Join(t.TempDir(), "sub", "ca.pem")
	cmd := newCAExportCmd()
	require.NoError(t, cmd.Flags().Set("out", outPath))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Equal(t, rootPEM, string(data))
}

func TestCAInfoRemoteUsesControlPlane(t *testing.T) {
	expiry := time.Now().Add(48 * time.Hour).UTC()
	rootPEM, fp := testCertPEM(t, true)
	cp := mocks.NewMockControlPlane(t)
	cp.EXPECT().GetCA(context.Background()).Return(&dto.CAResponse{
		RootPEM: rootPEM, RootCN: "ignored", Fingerprint: fp, IntermediateExpiry: expiry,
	}, nil).Once()
	stubCARemote(t, cp)

	cmd := newCAInfoCmd()
	require.NoError(t, cmd.Flags().Set("json", "true"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))

	assert.Contains(t, buf.String(), `"root_cn": "Remote Root"`)
	assert.Contains(t, buf.String(), `"fingerprint": "SHA256:`+fp+`"`)
}

func TestCAInfoRemoteText(t *testing.T) {
	rootPEM, fp := testCertPEM(t, true)
	cp := mocks.NewMockControlPlane(t)
	cp.EXPECT().GetCA(context.Background()).Return(&dto.CAResponse{
		RootPEM: rootPEM, Fingerprint: fp, IntermediateExpiry: time.Now().Add(time.Hour),
	}, nil).Once()
	stubCARemote(t, cp)

	cmd := newCAInfoCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))

	assert.Contains(t, buf.String(), "Remote Root")
	assert.Contains(t, buf.String(), "SHA256:"+fp)
}

func TestCARemoteErrorIsWrapped(t *testing.T) {
	cp := mocks.NewMockControlPlane(t)
	cp.EXPECT().GetCA(context.Background()).Return(nil, errors.New("internal TLS is disabled on this server")).Once()
	stubCARemote(t, cp)

	cmd := newCAInfoCmd()
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get CA from remote")
	assert.Contains(t, err.Error(), "internal TLS is disabled on this server")
}

func TestCAInstallRejectsRemote(t *testing.T) {
	// No GetCA expectation: the mock fails the test if the remote is contacted.
	stubCARemote(t, mocks.NewMockControlPlane(t))

	for _, uninstall := range []bool{false, true} {
		cmd := newCAInstallCmd()
		if uninstall {
			require.NoError(t, cmd.Flags().Set("uninstall", "true"))
		}
		cmd.SetContext(context.Background())
		err := cmd.RunE(cmd, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ca install only works on the Gordon host")
		assert.Contains(t, err.Error(), "gordon ca export --remote <name> --out gordon-ca.pem")
	}
}

func TestCARemoteRejectsInvalidRoot(t *testing.T) {
	nonCAPEM, nonCAFP := testCertPEM(t, false)
	goodPEM, _ := testCertPEM(t, true)
	twoPEM := goodPEM + goodPEM

	tests := []struct {
		name    string
		resp    dto.CAResponse
		wantErr string
	}{
		{name: "invalid pem", resp: dto.CAResponse{RootPEM: "not a pem"}, wantErr: "no CERTIFICATE PEM block"},
		{name: "garbage der", resp: dto.CAResponse{RootPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("junk")}))}, wantErr: "parse certificate"},
		{name: "wrong block type", resp: dto.CAResponse{RootPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")}))}, wantErr: "no CERTIFICATE PEM block"},
		{name: "multiple certs", resp: dto.CAResponse{RootPEM: twoPEM}, wantErr: "exactly one certificate"},
		{name: "not a CA", resp: dto.CAResponse{RootPEM: nonCAPEM, Fingerprint: nonCAFP}, wantErr: "not a CA"},
		{name: "fingerprint mismatch", resp: dto.CAResponse{RootPEM: goodPEM, Fingerprint: "AA:BB"}, wantErr: "does not match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := tt.resp
			cp := mocks.NewMockControlPlane(t)
			cp.EXPECT().GetCA(context.Background()).Return(&resp, nil).Once()

			_, err := fetchRemoteCADetails(context.Background(), cp)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid root CA")
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestCARemoteRejectsNotSelfSigned(t *testing.T) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Issuer"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
	}
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	// Subject claims CA, but it is signed by a different key than its own.
	subTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Sub"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, subTmpl, caTmpl, &otherKey.PublicKey, caKey)
	require.NoError(t, err)

	cp := mocks.NewMockControlPlane(t)
	cp.EXPECT().GetCA(context.Background()).Return(&dto.CAResponse{
		RootPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}, nil).Once()

	_, err = fetchRemoteCADetails(context.Background(), cp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not validly self-signed")
}

func TestCARemoteComputesFingerprintAndCNLocally(t *testing.T) {
	rootPEM, fp := testCertPEM(t, true)
	cp := mocks.NewMockControlPlane(t)
	// Server omits fingerprint and CN; both are derived from the certificate.
	cp.EXPECT().GetCA(context.Background()).Return(&dto.CAResponse{RootPEM: rootPEM}, nil).Once()

	details, err := fetchRemoteCADetails(context.Background(), cp)
	require.NoError(t, err)
	assert.Equal(t, "Remote Root", details.rootCN)
	assert.Equal(t, fp, details.fingerprint)
}

func TestCALocalErrorMentionsConfigAndRemote(t *testing.T) {
	stubCARemote(t, nil)

	cfg := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(cfg, []byte("[server]\nport = 8088\n"), 0600))
	// cliConfigPath is a package var: this test must not run in parallel.
	prev := cliConfigPath
	cliConfigPath = cfg
	t.Cleanup(func() { cliConfigPath = prev })

	cmd := newCAExportCmd()
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "internal TLS is disabled")
	assert.Contains(t, err.Error(), cfg)
	assert.Contains(t, err.Error(), "--remote")
}

func TestErrLocalInternalTLSDisabledWithoutConfigFile(t *testing.T) {
	err := errLocalInternalTLSDisabled("", true)
	assert.Contains(t, err.Error(), "no local server config file was found")
	assert.Contains(t, err.Error(), "--remote")

	// install is local-only, so it must not suggest --remote.
	assert.NotContains(t, errLocalInternalTLSDisabled("", false).Error(), "--remote")
}

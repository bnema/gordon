package app_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"testing"
	"time"

	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
	outboundary "github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	pkiusecase "github.com/bnema/gordon/internal/usecase/pki"
	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time check: stubRoutes implements the boundary interface.
var _ outboundary.RouteChecker = (*stubRoutes)(nil)

type stubRoutes struct {
	routes []domain.Route
}

func (s *stubRoutes) GetRoutes(_ context.Context) []domain.Route { return s.routes }
func (s *stubRoutes) GetExternalRoutes() map[string]string       { return nil }

func TestTLSHandshake_OnDemandCert(t *testing.T) {
	dir := t.TempDir()
	log := zerowrap.Default()

	ca, err := pkiadapter.NewCA(dir, log)
	require.NoError(t, err)

	routes := &stubRoutes{routes: []domain.Route{{Domain: "test.local"}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := pkiusecase.NewService(ctx, ca, routes, log)
	defer svc.Stop()

	// Start a TLS server using the PKI service
	tlsCfg := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: svc.GetCertificate,
	}

	srv := &http.Server{
		Handler:   http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }),
		TLSConfig: tlsCfg,
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	require.NoError(t, err)
	defer ln.Close()

	go srv.Serve(ln)
	defer srv.Close()

	// Build a client that trusts Gordon's root CA
	rootPool := x509.NewCertPool()
	rootPool.AppendCertsFromPEM(ca.RootCertificate())

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    rootPool,
				ServerName: "test.local",
			},
		},
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get("https://" + ln.Addr().String() + "/")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestTLSHandshake_UnknownDomain_Rejected(t *testing.T) {
	dir := t.TempDir()
	log := zerowrap.Default()

	ca, err := pkiadapter.NewCA(dir, log)
	require.NoError(t, err)

	routes := &stubRoutes{routes: []domain.Route{{Domain: "allowed.local"}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := pkiusecase.NewService(ctx, ca, routes, log)
	defer svc.Stop()

	tlsCfg := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: svc.GetCertificate,
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	require.NoError(t, err)
	defer ln.Close()

	srv := &http.Server{Handler: http.NotFoundHandler(), TLSConfig: tlsCfg}
	go srv.Serve(ln)
	defer srv.Close()

	rootPool := x509.NewCertPool()
	rootPool.AppendCertsFromPEM(ca.RootCertificate())

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    rootPool,
				ServerName: "unknown.local", // not in route table
			},
		},
		Timeout: 5 * time.Second,
	}

	_, err = client.Get("https://" + ln.Addr().String() + "/")
	assert.Error(t, err, "TLS handshake should fail for unknown domain")
}

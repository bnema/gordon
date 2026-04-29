package in

import (
	"context"
	"crypto/tls"
	"testing"

	"github.com/bnema/gordon/internal/domain"
)

// publicTLSStub is a compile-time stub for PublicTLSService.
type publicTLSStub struct{}

func (s publicTLSStub) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	return nil, nil
}

func (s publicTLSStub) GetHTTP01Challenge(ctx context.Context, token string) (string, bool) {
	return "", false
}

func (s publicTLSStub) Status(ctx context.Context) domain.PublicTLSStatus {
	return domain.PublicTLSStatus{}
}

func (s publicTLSStub) Reconcile(ctx context.Context) error {
	return nil
}

func (s publicTLSStub) Stop(ctx context.Context) error {
	return nil
}

// TestPublicTLSServiceInterface asserts that publicTLSStub satisfies PublicTLSService.
func TestPublicTLSServiceInterface(t *testing.T) {
	var _ PublicTLSService = publicTLSStub{}
}

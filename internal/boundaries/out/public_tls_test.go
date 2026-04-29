package out

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// issuerStub implements PublicCertificateIssuer.
type issuerStub struct{}

func (s issuerStub) Obtain(ctx context.Context, order CertificateOrder) (*StoredCertificate, error) {
	return nil, nil
}

func (s issuerStub) Renew(ctx context.Context, cert StoredCertificate) (*StoredCertificate, error) {
	return nil, nil
}

// storeStub implements CertificateStore.
type storeStub struct{}

func (s storeStub) LoadAccount(ctx context.Context) (*ACMEAccount, error) {
	return nil, nil
}

func (s storeStub) SaveAccount(ctx context.Context, account ACMEAccount) error {
	return nil
}

func (s storeStub) LoadAll(ctx context.Context) ([]StoredCertificate, error) {
	return nil, nil
}

func (s storeStub) Save(ctx context.Context, cert StoredCertificate) error {
	return nil
}

func (s storeStub) Lock(ctx context.Context) (func() error, error) {
	return func() error { return nil }, nil
}

// secretStub implements SecretResolver.
type secretStub struct{}

func (s secretStub) ResolveCloudflareToken(ctx context.Context) (SecretValue, error) {
	return SecretValue{}, nil
}

// zoneStub implements CloudflareZoneResolver.
type zoneStub struct{}

func (s zoneStub) FindZone(ctx context.Context, domainName string) (CloudflareZone, error) {
	return CloudflareZone{}, nil
}

// TestPublicTLSOutputInterfaces asserts that all stubs satisfy their respective interfaces
// and that the required types are importable.
func TestPublicTLSOutputInterfaces(t *testing.T) {
	var _ PublicCertificateIssuer = issuerStub{}
	var _ CertificateStore = storeStub{}
	var _ SecretResolver = secretStub{}
	var _ CloudflareZoneResolver = zoneStub{}

	// Verify that referenced types are importable
	var _ tls.Certificate
	var _ time.Time
	_ = domain.ACMEChallengeHTTP01
}

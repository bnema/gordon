// Package acmelego implements ACME certificate operations using the lego library.
package acmelego

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/acme"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/registration"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// Config holds the configuration for the ACME certificate issuer.
type Config struct {
	// Email is the ACME account contact email.
	Email string

	// Challenge is the ACME challenge mode to use.
	Challenge domain.ACMEChallengeMode

	// Token is the Cloudflare API token (required for DNS-01 challenge).
	Token string

	// Store persists ACME accounts and certificates.
	Store out.CertificateStore

	// HTTPChallengeSink is used for HTTP-01 challenge token storage.
	HTTPChallengeSink HTTPChallengeSink

	// CADirectoryURL is the ACME directory URL. If empty, letsencrypt production is used.
	CADirectoryURL string

	// HTTPClient is an optional HTTP client for the ACME client. If nil, a default is used.
	HTTPClient *http.Client
}

// Issuer implements out.PublicCertificateIssuer using the lego ACME client.
type Issuer struct {
	cfg    Config
	mu     sync.Mutex
	client *lego.Client
	user   *AccountUser
}

// NewIssuer creates a new Issuer. Config is validated; the ACME account is
// loaded or created lazily on first Obtain or Renew call.
func NewIssuer(cfg Config) (*Issuer, error) {
	if cfg.Email == "" {
		return nil, fmt.Errorf("acmelego: %w", domain.ErrACMEEmailRequired)
	}
	if cfg.Store == nil {
		return nil, errors.New("acmelego: certificate store is required")
	}
	if cfg.Challenge == "" {
		cfg.Challenge = domain.ACMEChallengeHTTP01
	}
	if cfg.Challenge == domain.ACMEChallengeCloudflareDNS01 && cfg.Token == "" {
		return nil, fmt.Errorf("acmelego: %w", domain.ErrCloudflareTokenMissing)
	}

	return &Issuer{cfg: cfg}, nil
}

// compile-time interface check
var _ out.PublicCertificateIssuer = (*Issuer)(nil)

// Obtain obtains a new certificate for the given order. The ACME client is
// initialized lazily on the first call.
func (i *Issuer) Obtain(ctx context.Context, order out.CertificateOrder) (*out.StoredCertificate, error) {
	if err := i.ensureClient(ctx); err != nil {
		return nil, err
	}

	resource, err := i.client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: order.Names,
		Bundle:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("obtain certificate: %w", err)
	}

	return resourceToStored(order, resource), nil
}

// Renew renews an existing certificate. The ACME client is initialized lazily
// on the first call. Bundle and MustStaple are set to true/false respectively.
func (i *Issuer) Renew(ctx context.Context, cert out.StoredCertificate) (*out.StoredCertificate, error) {
	if err := i.ensureClient(ctx); err != nil {
		return nil, err
	}

	domain := ""
	if len(cert.Names) > 0 {
		domain = cert.Names[0]
	}

	legoCert := certificate.Resource{
		Domain:            domain,
		PrivateKey:        cert.PrivateKeyPEM,
		Certificate:       cert.CertPEM,
		IssuerCertificate: cert.ChainPEM,
	}

	// If FullchainPEM is available but CertPEM is empty, use FullchainPEM.
	if len(legoCert.Certificate) == 0 && len(cert.FullchainPEM) > 0 {
		legoCert.Certificate = cert.FullchainPEM
	}

	renewed, err := i.client.Certificate.RenewWithOptions(legoCert, &certificate.RenewOptions{
		Bundle: true,
	})
	if err != nil {
		return nil, fmt.Errorf("renew certificate: %w", err)
	}

	// Preserve the original order info
	order := out.CertificateOrder{
		ID:        cert.ID,
		Names:     cert.Names,
		Challenge: cert.Challenge,
	}

	return resourceToStored(order, renewed), nil
}

// ensureClient initializes the lego client and ACME account if not yet done.
func (i *Issuer) ensureClient(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.client != nil {
		return nil
	}

	// Load or create ACME account
	user, err := i.loadOrCreateAccount(ctx)
	if err != nil {
		return fmt.Errorf("acme account: %w", err)
	}
	i.user = user

	// Build lego config
	legoCfg := lego.NewConfig(user)
	legoCfg.Certificate.KeyType = certcrypto.EC256 // ECDSA P-256

	if i.cfg.CADirectoryURL != "" {
		legoCfg.CADirURL = i.cfg.CADirectoryURL
	}
	if i.cfg.HTTPClient != nil {
		legoCfg.HTTPClient = i.cfg.HTTPClient
	}

	client, err := lego.NewClient(legoCfg)
	if err != nil {
		return fmt.Errorf("create lego client: %w", err)
	}

	// Set up challenge providers
	switch i.cfg.Challenge {
	case domain.ACMEChallengeHTTP01:
		if i.cfg.HTTPChallengeSink != nil {
			if err := client.Challenge.SetHTTP01Provider(NewHTTPProvider(i.cfg.HTTPChallengeSink)); err != nil {
				return fmt.Errorf("set http-01 provider: %w", err)
			}
		}
	case domain.ACMEChallengeCloudflareDNS01:
		cfCfg := cloudflare.NewDefaultConfig()
		cfCfg.AuthToken = i.cfg.Token
		cfProvider, err := cloudflare.NewDNSProviderConfig(cfCfg)
		if err != nil {
			return fmt.Errorf("create cloudflare dns provider: %w", err)
		}
		if err := client.Challenge.SetDNS01Provider(cfProvider); err != nil {
			return fmt.Errorf("set dns-01 provider: %w", err)
		}
	}

	i.client = client
	return nil
}

// loadOrCreateAccount loads an existing ACME account from the store or creates
// a new one. If creating, it registers the account with the ACME server.
func (i *Issuer) loadOrCreateAccount(ctx context.Context) (*AccountUser, error) {
	stored, err := i.cfg.Store.LoadAccount(ctx)
	if err != nil {
		return nil, fmt.Errorf("load account: %w", err)
	}

	if stored != nil && len(stored.PrivateKeyPEM) > 0 {
		return restoreAccount(stored)
	}

	// Generate new ECDSA P-256 private key for the ACME account
	privateKey, err := certcrypto.GeneratePrivateKey(certcrypto.EC256)
	if err != nil {
		return nil, fmt.Errorf("generate account key: %w", err)
	}

	user := NewAccountUser(i.cfg.Email, privateKey, nil)

	// We need a temporary client to register the account
	legoCfg := lego.NewConfig(user)
	legoCfg.Certificate.KeyType = certcrypto.EC256
	if i.cfg.CADirectoryURL != "" {
		legoCfg.CADirURL = i.cfg.CADirectoryURL
	}
	if i.cfg.HTTPClient != nil {
		legoCfg.HTTPClient = i.cfg.HTTPClient
	}

	tmpClient, err := lego.NewClient(legoCfg)
	if err != nil {
		return nil, fmt.Errorf("create temporary lego client: %w", err)
	}

	reg, err := tmpClient.Registration.Register(registration.RegisterOptions{
		TermsOfServiceAgreed: true,
	})
	if err != nil {
		return nil, fmt.Errorf("register acme account: %w", err)
	}

	user = NewAccountUser(i.cfg.Email, privateKey, reg)

	// Persist the account
	bodyJSON, err := json.Marshal(reg.Body)
	if err != nil {
		return nil, fmt.Errorf("marshal account body: %w", err)
	}

	privateKeyPEM := certcrypto.PEMEncode(privateKey)

	if err := i.cfg.Store.SaveAccount(ctx, out.ACMEAccount{
		Email:           i.cfg.Email,
		PrivateKeyPEM:   privateKeyPEM,
		RegistrationURI: reg.URI,
		BodyJSON:        bodyJSON,
	}); err != nil {
		return nil, fmt.Errorf("save account: %w", err)
	}

	return user, nil
}

// restoreAccount reconstructs an AccountUser from a stored ACMEAccount.
func restoreAccount(stored *out.ACMEAccount) (*AccountUser, error) {
	privateKey, err := certcrypto.ParsePEMPrivateKey(stored.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse account private key: %w", err)
	}

	reg := &registration.Resource{
		URI: stored.RegistrationURI,
	}

	if len(stored.BodyJSON) > 0 {
		var body acme.Account
		if err := json.Unmarshal(stored.BodyJSON, &body); err != nil {
			return nil, fmt.Errorf("unmarshal account body: %w", err)
		}
		reg.Body = body
	}

	return NewAccountUser(stored.Email, privateKey, reg), nil
}

// resourceToStored converts a lego certificate.Resource to out.StoredCertificate.
func resourceToStored(order out.CertificateOrder, res *certificate.Resource) *out.StoredCertificate {
	var fullchainPEM []byte
	if len(res.IssuerCertificate) > 0 {
		fullchainPEM = append(append([]byte{}, res.Certificate...), res.IssuerCertificate...)
	} else {
		fullchainPEM = res.Certificate
	}

	// Parse the certificate to extract NotAfter and populate tls.Certificate.
	var tlsCert tls.Certificate
	notAfter := time.Time{}
	if parsedTLSCert, err := tls.X509KeyPair(fullchainPEM, res.PrivateKey); err == nil && len(parsedTLSCert.Certificate) > 0 {
		tlsCert = parsedTLSCert
		if parsed, parseErr := x509.ParseCertificate(parsedTLSCert.Certificate[0]); parseErr == nil {
			notAfter = parsed.NotAfter
		}
	}

	names := order.Names
	if names == nil {
		names = []string{}
	}

	return &out.StoredCertificate{
		ID:            order.ID,
		Names:         names,
		Challenge:     order.Challenge,
		Certificate:   tlsCert,
		CertPEM:       res.Certificate,
		ChainPEM:      res.IssuerCertificate,
		FullchainPEM:  fullchainPEM,
		PrivateKeyPEM: res.PrivateKey,
		NotAfter:      notAfter,
	}
}

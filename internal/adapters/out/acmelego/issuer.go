// Package acmelego implements ACME certificate operations using the lego library.
package acmelego

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/acme"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/dns01"
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

	// DNSResolvers are recursive resolvers used for DNS-01 propagation checks.
	DNSResolvers []string

	// DNSPropagationTimeout is the maximum wait for DNS-01 TXT propagation.
	DNSPropagationTimeout time.Duration

	// DNSPollingInterval is the interval between DNS-01 propagation checks.
	DNSPollingInterval time.Duration

	// Store persists ACME accounts and certificates.
	Store out.CertificateStore

	// HTTPChallengeSink is used for HTTP-01 challenge token storage.
	HTTPChallengeSink out.HTTPChallengeSink

	// CADirectoryURL is the ACME directory URL. If empty, letsencrypt production is used.
	CADirectoryURL string

	// HTTPClient is an optional HTTP client for the ACME client. If nil, a default is used.
	HTTPClient *http.Client
}

// Issuer implements out.PublicCertificateIssuer using the lego ACME client.
type Issuer struct {
	cfg          Config
	mu           sync.Mutex
	client       *lego.Client
	user         *AccountUser
	initializing bool
	initDone     chan struct{}
}

// NewIssuer creates a new Issuer. Config is validated; the ACME account is
// loaded or created lazily on first Obtain or Renew call.
func NewIssuer(cfg Config) (*Issuer, error) {
	if cfg.Email == "" {
		return nil, fmt.Errorf("acmelego: %w", domain.ErrACMEEmailRequired)
	}
	if cfg.Store == nil {
		return nil, fmt.Errorf("acmelego: %w", domain.ErrCertificateStoreRequired)
	}
	if cfg.CADirectoryURL != "" && !strings.HasPrefix(cfg.CADirectoryURL, "https://") {
		return nil, fmt.Errorf("acmelego: CADirectoryURL must use HTTPS, got %q", cfg.CADirectoryURL)
	}
	if cfg.Challenge == "" {
		cfg.Challenge = domain.ACMEChallengeHTTP01
	}
	switch cfg.Challenge {
	case domain.ACMEChallengeHTTP01:
		if cfg.HTTPChallengeSink == nil {
			return nil, fmt.Errorf("acmelego: %w", domain.ErrHTTPChallengeSinkRequired)
		}
	case domain.ACMEChallengeCloudflareDNS01:
		if cfg.Token == "" {
			return nil, fmt.Errorf("acmelego: %w", domain.ErrCloudflareTokenMissing)
		}
		resolvers, err := normalizeDNSResolvers(cfg.DNSResolvers)
		if err != nil {
			return nil, err
		}
		cfg.DNSResolvers = resolvers
		if cfg.DNSPropagationTimeout <= 0 {
			return nil, fmt.Errorf("acmelego: %w: DNSPropagationTimeout must be positive", domain.ErrDNSConfigInvalid)
		}
		if cfg.DNSPollingInterval <= 0 {
			return nil, fmt.Errorf("acmelego: %w: DNSPollingInterval must be positive", domain.ErrDNSConfigInvalid)
		}
		if cfg.DNSPollingInterval >= cfg.DNSPropagationTimeout {
			return nil, fmt.Errorf("acmelego: %w: DNSPollingInterval must be less than DNSPropagationTimeout", domain.ErrDNSConfigInvalid)
		}
	default:
		return nil, fmt.Errorf("acmelego: %w: %s", domain.ErrACMEChallengeInvalid, cfg.Challenge)
	}

	return &Issuer{cfg: cfg}, nil
}

func normalizeDNSResolvers(resolvers []string) ([]string, error) {
	if len(resolvers) == 0 {
		return nil, fmt.Errorf("acmelego: %w: DNSResolvers must contain at least one resolver", domain.ErrDNSConfigInvalid)
	}

	normalized := make([]string, len(resolvers))
	for i, resolver := range resolvers {
		trimmed := strings.TrimSpace(resolver)
		if trimmed == "" {
			return nil, fmt.Errorf("acmelego: %w: invalid DNSResolvers entry", domain.ErrDNSConfigInvalid)
		}
		host, port, err := net.SplitHostPort(trimmed)
		if err != nil {
			return nil, fmt.Errorf("acmelego: %w: invalid DNSResolvers entry", domain.ErrDNSConfigInvalid)
		}
		if host == "" {
			return nil, fmt.Errorf("acmelego: %w: invalid DNSResolvers entry", domain.ErrDNSConfigInvalid)
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return nil, fmt.Errorf("acmelego: %w: invalid DNSResolvers entry", domain.ErrDNSConfigInvalid)
		}
		normalized[i] = trimmed
	}

	return normalized, nil
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

	return resourceToStored(order, resource)
}

// Renew renews an existing certificate. The ACME client is initialized lazily
// on the first call. Bundle and MustStaple are set to true/false respectively.
func (i *Issuer) Renew(ctx context.Context, cert out.StoredCertificate) (*out.StoredCertificate, error) {
	if err := i.ensureClient(ctx); err != nil {
		return nil, err
	}

	domainName := ""
	if len(cert.Names) > 0 {
		domainName = cert.Names[0]
	}

	legoCert := certificate.Resource{
		Domain:            domainName,
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

	return resourceToStored(order, renewed)
}

// ensureClient initializes the lego client and ACME account if not yet done.
func (i *Issuer) ensureClient(ctx context.Context) error {
	for {
		i.mu.Lock()
		if i.client != nil {
			i.mu.Unlock()
			return nil
		}
		if i.initializing {
			done := i.initDone
			i.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		i.initializing = true
		i.initDone = make(chan struct{})
		done := i.initDone
		i.mu.Unlock()

		user, client, err := i.buildClient(ctx)

		i.mu.Lock()
		if err == nil {
			i.user = user
			i.client = client
		}
		i.initializing = false
		close(done)
		i.initDone = nil
		i.mu.Unlock()

		return err
	}
}

func (i *Issuer) buildClient(ctx context.Context) (*AccountUser, *lego.Client, error) {
	// Load or create ACME account
	user, err := i.loadOrCreateAccount(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acme account: %w", err)
	}

	client, err := lego.NewClient(i.newLegoConfig(user))
	if err != nil {
		return nil, nil, fmt.Errorf("create lego client: %w", err)
	}

	// Set up challenge providers
	switch i.cfg.Challenge {
	case domain.ACMEChallengeHTTP01:
		if err := client.Challenge.SetHTTP01Provider(NewHTTPProvider(i.cfg.HTTPChallengeSink)); err != nil {
			return nil, nil, fmt.Errorf("set http-01 provider: %w", err)
		}
	case domain.ACMEChallengeCloudflareDNS01:
		cfProvider, err := cloudflare.NewDNSProviderConfig(newCloudflareDNSProviderConfig(i.cfg))
		if err != nil {
			return nil, nil, fmt.Errorf("create cloudflare dns provider: %w", err)
		}
		if err := client.Challenge.SetDNS01Provider(cfProvider, dns01.AddRecursiveNameservers(i.cfg.DNSResolvers)); err != nil {
			return nil, nil, fmt.Errorf("set dns-01 provider: %w", err)
		}
	}

	return user, client, nil
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
	tmpClient, err := lego.NewClient(i.newLegoConfig(user))
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

// newLegoConfig creates a lego.Config with common settings from the Issuer config.
func (i *Issuer) newLegoConfig(user *AccountUser) *lego.Config {
	legoCfg := lego.NewConfig(user)
	legoCfg.Certificate.KeyType = certcrypto.EC256 // ECDSA P-256
	if i.cfg.CADirectoryURL != "" {
		legoCfg.CADirURL = i.cfg.CADirectoryURL
	}
	if i.cfg.HTTPClient != nil {
		legoCfg.HTTPClient = i.cfg.HTTPClient
	}
	return legoCfg
}

// newCloudflareDNSProviderConfig creates a cloudflare DNS provider config from the issuer config.
func newCloudflareDNSProviderConfig(cfg Config) *cloudflare.Config {
	cfCfg := cloudflare.NewDefaultConfig()
	cfCfg.AuthToken = cfg.Token
	cfCfg.PropagationTimeout = cfg.DNSPropagationTimeout
	cfCfg.PollingInterval = cfg.DNSPollingInterval
	return cfCfg
}

// resourceToStored converts a lego certificate.Resource to out.StoredCertificate.
func resourceToStored(order out.CertificateOrder, res *certificate.Resource) (*out.StoredCertificate, error) {
	var fullchainPEM []byte
	if len(res.IssuerCertificate) > 0 {
		fullchainPEM = append(append([]byte{}, res.Certificate...), res.IssuerCertificate...)
	} else {
		fullchainPEM = res.Certificate
	}

	// Parse the certificate to extract NotAfter and populate tls.Certificate.
	var tlsCert tls.Certificate
	notAfter := time.Time{}
	parsedTLSCert, tlsErr := tls.X509KeyPair(fullchainPEM, res.PrivateKey)
	if tlsErr != nil {
		return nil, fmt.Errorf("parse tls certificate: %w", tlsErr)
	}
	tlsCert = parsedTLSCert
	if len(parsedTLSCert.Certificate) > 0 {
		parsed, parseErr := x509.ParseCertificate(parsedTLSCert.Certificate[0])
		if parseErr != nil {
			return nil, fmt.Errorf("parse x509 certificate: %w", parseErr)
		}
		notAfter = parsed.NotAfter
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
	}, nil
}

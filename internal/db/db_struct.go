package db

type User struct {
	ID      string `sql:"id, primary_key"`
	Name    string `sql:"name"`
	Email   string `sql:"email"`
	Account Account
}

type Account struct {
	ID        string `sql:"id, primary_key"`
	UserID    string `sql:"user_id, foreign_key=user.id"`
	Sessions  []Sessions
	Providers []Provider
	Clients   []Clients
}

type Sessions struct {
	ID          string `sql:"id, primary_key"`
	AccountID   string `sql:"account_id, foreign_key=account.id"`
	BrowserInfo string `sql:"browser_info"`
	AccessToken string `sql:"access_token"`
	Expires     string `sql:"expires"`
	IsOnline    bool   `sql:"is_online"`
}

type Provider struct {
	ID         string `sql:"id, primary_key"`
	AccountID  string `sql:"account_id, foreign_key=account.id"`
	Name       string `sql:"name"`
	Login      string `sql:"login"`
	AvatarURL  string `sql:"avatar_url"`
	ProfileURL string `sql:"profile_url"`
	Email      string `sql:"email"`
}

type Clients struct {
	ID        string `sql:"id, primary_key"`
	AccountID string `sql:"account_id, foreign_key=account.id"`
	OS        string `sql:"os"`
	IP        string `sql:"ip"`
	Hostname  string `sql:"hostname"`
	Expires   string `sql:"expires"`
}

// Domain represents a domain configuration for the proxy
type Domain struct {
	ID        string `sql:"id, primary_key"`
	Name      string `sql:"name"` // e.g. example.com
	AccountID string `sql:"account_id, foreign_key=account.id"`
	CreatedAt string `sql:"created_at"`
	UpdatedAt string `sql:"updated_at"`
}

// Certificate represents a TLS certificate for a domain
type Certificate struct {
	ID        string `sql:"id, primary_key"`
	DomainID  string `sql:"domain_id, foreign_key=domain.id"`
	CertFile  string `sql:"cert_file"` // Path to the certificate file
	KeyFile   string `sql:"key_file"`  // Path to the private key file
	IssuedAt  string `sql:"issued_at"`
	ExpiresAt string `sql:"expires_at"`
	Issuer    string `sql:"issuer"` // e.g. "Let's Encrypt"
	Status    string `sql:"status"` // e.g. valid, expired, revoked
}

// ProxyRoute represents a mapping from a domain to a container
type ProxyRoute struct {
	ID            string `sql:"id, primary_key"`
	DomainID      string `sql:"domain_id, foreign_key=domain.id"`
	ContainerID   string `sql:"container_id"`
	ContainerIP   string `sql:"container_ip"`
	ContainerPort string `sql:"container_port"`
	Protocol      string `sql:"protocol"` // http or https
	Path          string `sql:"path"`     // path prefix to route (e.g., /api)
	Active        bool   `sql:"active"`
	CreatedAt     string `sql:"created_at"`
	UpdatedAt     string `sql:"updated_at"`
}

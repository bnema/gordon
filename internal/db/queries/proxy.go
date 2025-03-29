package queries

// ProxyQueries contains all SQL queries used by the proxy package
type ProxyQueries struct {
	// Route queries
	GetActiveRoutes                      string
	GetRouteByDomain                     string
	GetRouteIDAndContainerIDByDomainName string
	GetRouteIPByID                       string
	InsertDomain                         string
	GetDomainByName                      string
	GetFirstAccount                      string
	UpdateRoute                          string
	UpdateRecreatedRoute                 string
	InsertRoute                          string
	DeleteRouteByDomainID                string
	DeleteDomainByID                     string
	UpdateRouteIP                        string
	GetAllRoutes                         string
	MarkRouteInactive                    string

	// ACME queries
	GetDomainAcmeConfig    string
	UpdateDomainAcmeConfig string
	GetCertificateByDomain string
	UpdateCertificate      string

	// Admin domain queries
	CheckDomainExists     string
	CreateAdminDomain     string
	UpdateAdminDomainAcme string

	// ACME Account queries (NEW)
	GetAcmeAccountByEmail      string
	InsertOrReplaceAcmeAccount string
}

// NewProxyQueries returns a new instance of ProxyQueries
func NewProxyQueries() *ProxyQueries {
	return &ProxyQueries{
		// Route queries
		GetActiveRoutes: `
			SELECT pr.id, d.name, pr.container_id, pr.container_ip, pr.container_port, pr.protocol, pr.path, pr.active
			FROM proxy_route pr
			JOIN domain d ON pr.domain_id = d.id
			WHERE pr.active = 1
		`,
		GetRouteByDomain: `
			SELECT id, container_id, container_port 
			FROM proxy_route
			WHERE domain_id = ?
		`,
		GetRouteIDAndContainerIDByDomainName: `
			SELECT pr.id, pr.container_id 
			FROM proxy_route pr 
			JOIN domain d ON pr.domain_id = d.id 
			WHERE d.name = ?
		`,
		GetRouteIPByID: `
			SELECT container_ip 
			FROM proxy_route 
			WHERE id = ?
		`,
		InsertDomain: `
			INSERT INTO domain (id, name, account_id, created_at, updated_at) 
			VALUES (?, ?, ?, ?, ?)
		`,
		GetDomainByName: `
			SELECT id FROM domain WHERE name = ?
		`,
		GetFirstAccount: `
			SELECT id FROM account LIMIT 1
		`,
		UpdateRoute: `
			UPDATE proxy_route SET 
				container_id = ?, 
				container_ip = ?, 
				container_port = ?, 
				protocol = ?, 
				path = ?, 
				active = ?, 
				updated_at = ? 
			WHERE id = ?
		`,
		UpdateRecreatedRoute: `
			UPDATE proxy_route 
			SET container_id = ?, container_ip = ?, container_port = ?, updated_at = ? 
			WHERE id = ?
		`,
		InsertRoute: `
			INSERT INTO proxy_route (
				id, domain_id, container_id, container_ip, container_port, 
				protocol, path, active, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
		DeleteRouteByDomainID: `
			DELETE FROM proxy_route WHERE domain_id = ?
		`,
		DeleteDomainByID: `
			DELETE FROM domain WHERE id = ?
		`,
		UpdateRouteIP: `
			UPDATE proxy_route SET container_ip = ?, active = 1, updated_at = ? 
			WHERE domain_id = ? AND container_id = ?
		`,
		GetAllRoutes: `
			SELECT pr.id, d.name, pr.container_id, pr.container_ip, pr.container_port, pr.protocol, pr.path, pr.active
			FROM proxy_route pr
			JOIN domain d ON pr.domain_id = d.id
		`,
		MarkRouteInactive: `
			UPDATE proxy_route SET active = 0, updated_at = ? WHERE domain_id = ?
		`,

		// ACME queries
		GetDomainAcmeConfig: `
			SELECT acme_enabled, acme_challenge_type, acme_dns_provider, acme_dns_credentials_ref
			FROM domain WHERE name = ?
		`,
		UpdateDomainAcmeConfig: `
			UPDATE domain SET 
				acme_enabled = ?,
				acme_challenge_type = ?,
				acme_dns_provider = ?,
				acme_dns_credentials_ref = ?,
				updated_at = ?
			WHERE name = ?
		`,
		GetCertificateByDomain: `
			-- Reverted: Select all original columns as expected by checkExistingCertificate
			SELECT c.cert_file, c.key_file, c.issued_at, c.expires_at, c.issuer, c.status
			FROM certificate c
			JOIN domain d ON c.domain_id = d.id
			WHERE d.name = ?
			-- Note: This query might return multiple rows or non-valid certs.
			-- The calling code (checkExistingCertificate and GetCertificate) needs to handle this.
		`,
		UpdateCertificate: `
			INSERT OR REPLACE INTO certificate (
				id, domain_id, cert_file, key_file, issued_at, expires_at, issuer, status
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`,

		// Admin domain queries
		CheckDomainExists: `
			SELECT EXISTS(SELECT 1 FROM domain WHERE name = ?)
		`,
		CreateAdminDomain: `
			INSERT INTO domain (
				id, name, account_id, created_at, updated_at, acme_enabled, acme_challenge_type
			) VALUES (?, ?, ?, ?, ?, ?, ?)
		`,
		UpdateAdminDomainAcme: `
			UPDATE domain SET 
				acme_enabled = 1,
				acme_challenge_type = 'http-01',
				updated_at = ?
			WHERE name = ?
		`,

		// ACME Account queries (NEW)
		GetAcmeAccountByEmail: `
			SELECT private_key, registration_info
			FROM acme_account
			WHERE email = ?
		`,
		InsertOrReplaceAcmeAccount: `
			INSERT OR REPLACE INTO acme_account (
				email, private_key, registration_info, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?)
		`,
	}
}

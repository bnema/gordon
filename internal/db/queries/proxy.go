package queries

// ProxyQueries has been moved to the internal/proxy package to avoid import cycles
// See internal/proxy/core.go for the implementation

// ProxyQueries contains all SQL queries used by the proxy package
type ProxyQueries struct {
	// Route queries
	GetActiveRoutes             string
	GetRouteByDomain            string
	InsertDomain                string
	GetDomainByName             string
	GetFirstAccount             string
	UpdateRoute                 string
	InsertRoute                 string
	DeleteRouteByDomainID       string
	DeleteDomainByID            string
	UpdateRouteIP               string
	GetAllRoutes                string
	MarkRouteInactive           string
	FindRoutesByContainerID     string
	GetAllActiveRoutesWithDetails string
	GetAllContainerIDs          string
	GetRouteByDomainName        string
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
			SELECT d.name, pr.container_id, pr.container_ip, pr.container_port, pr.protocol, pr.path
			FROM proxy_route pr
			JOIN domain d ON pr.domain_id = d.id
			WHERE pr.active = 1
		`,
		MarkRouteInactive: `
			UPDATE proxy_route SET active = 0, updated_at = ? WHERE domain_id = ?
		`,
		FindRoutesByContainerID: `
			SELECT pr.id, d.name, pr.container_ip, pr.container_port, pr.protocol, pr.path, pr.active
			FROM proxy_route pr
			JOIN domain d ON pr.domain_id = d.id
			WHERE pr.container_id = ?
		`,
		GetAllActiveRoutesWithDetails: `
			SELECT pr.id, d.name, pr.container_id, pr.container_ip, pr.container_port, pr.protocol, pr.path, pr.active
			FROM proxy_route pr
			JOIN domain d ON pr.domain_id = d.id
			WHERE pr.active = 1
		`,
		GetAllContainerIDs: `
			SELECT DISTINCT container_id
			FROM proxy_route
			WHERE active = 1
		`,
		GetRouteByDomainName: `
			SELECT pr.id, pr.container_id, pr.container_ip, pr.container_port, pr.protocol, pr.path, pr.active
			FROM proxy_route pr
			JOIN domain d ON pr.domain_id = d.id
			WHERE d.name = ?
		`,
	}
}

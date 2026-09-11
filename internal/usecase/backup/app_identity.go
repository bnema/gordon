package backup

// App-identity backup targets (v2.50 declarative-apps cutover).
//
// Detection resolves databases from the app ACTIVE record through
// appSources, without attachments. The resulting DBInfo carries app
// identity; domain-keyed callers match sources by served HTTP host and
// keep the domain storage key.

import (
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// AppDatabaseSource describes one running service from the app ACTIVE
// record: the fields backup detection needs, without attachments.
type AppDatabaseSource struct {
	App         string
	Service     string
	Name        string
	Image       string
	ContainerID string
	Ports       []int
	// Hosts are the HTTP hosts this service serves (eff.Spec.HTTP).
	// Domain-keyed backup callers filter sources by host.
	Hosts []string
}

// postgresAliases contains substrings that identify a PostgreSQL image or service.
var postgresAliases = []string{"postgres", "postgresql", "pgsql", "postgis"}

// DetectAppDatabases inspects app service descriptors and returns the
// detected databases. The resulting DBInfo carries App/Service identity;
// DetectDatabases fills the domain storage key for domain-keyed callers.
func DetectAppDatabases(sources []AppDatabaseSource) []domain.DBInfo {
	dbs := make([]domain.DBInfo, 0, len(sources))
	for _, source := range sources {
		if db, ok := detectDatabaseFromAppSource(source); ok {
			dbs = append(dbs, db)
		}
	}
	return dbs
}

// looksLikePostgres returns true if s contains any known PostgreSQL alias.
func looksLikePostgres(s string) bool {
	for _, alias := range postgresAliases {
		if strings.Contains(s, alias) {
			return true
		}
	}
	return false
}

// hasPort returns true if the port list contains p.
func hasPort(ports []int, p int) bool {
	for _, port := range ports {
		if port == p {
			return true
		}
	}
	return false
}

// postgresVersionFromImage extracts the leading numeric version from an
// image tag (postgres:16 -> "16").
func postgresVersionFromImage(image string) string {
	lastColon := strings.LastIndex(image, ":")
	if lastColon == -1 {
		return ""
	}
	tag := strings.TrimSpace(image[lastColon+1:])
	if tag == "" {
		return ""
	}
	for i := 0; i < len(tag); i++ {
		if tag[i] < '0' || tag[i] > '9' {
			if i == 0 {
				return ""
			}
			return tag[:i]
		}
	}
	return tag
}

func detectDatabaseFromAppSource(source AppDatabaseSource) (domain.DBInfo, bool) {
	image := strings.ToLower(source.Image)
	name := strings.ToLower(source.Name)

	switch {
	case looksLikePostgres(image) || looksLikePostgres(name):
		return buildAppPostgresInfo(source), true
	case hasPort(source.Ports, 5432):
		// Port 5432 fallback: likely PostgreSQL even with non-standard naming.
		return buildAppPostgresInfo(source), true
	default:
		return domain.DBInfo{}, false
	}
}

func buildAppPostgresInfo(source AppDatabaseSource) domain.DBInfo {
	port := 5432
	for _, p := range source.Ports {
		if p == 5432 {
			port = 5432
			break
		}
		if p > 0 && port == 5432 {
			port = p
		}
	}

	return domain.DBInfo{
		Type:        domain.DBTypePostgreSQL,
		Version:     postgresVersionFromImage(source.Image),
		App:         source.App,
		Service:     source.Service,
		Name:        source.Name,
		Host:        source.Name,
		Port:        port,
		ContainerID: source.ContainerID,
		ImageName:   source.Image,
	}
}

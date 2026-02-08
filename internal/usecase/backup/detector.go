package backup

import (
	"slices"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// postgresAliases contains substrings that identify a PostgreSQL image or service.
var postgresAliases = []string{"postgres", "postgresql", "pgsql", "postgis"}

// detectDatabaseFromAttachment maps a container attachment to DB metadata.
func detectDatabaseFromAttachment(domainName string, a domain.Attachment) (domain.DBInfo, bool) {
	image := strings.ToLower(a.Image)
	name := strings.ToLower(a.Name)

	switch {
	case looksLikePostgres(image) || looksLikePostgres(name):
		return buildPostgresInfo(domainName, a), true
	case hasPort(a.Ports, 5432):
		// Port 5432 fallback: likely PostgreSQL even with non-standard naming.
		return buildPostgresInfo(domainName, a), true
	default:
		return domain.DBInfo{}, false
	}
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
	return slices.Contains(ports, p)
}

// buildPostgresInfo constructs a DBInfo for a PostgreSQL attachment.
func buildPostgresInfo(domainName string, a domain.Attachment) domain.DBInfo {
	port := 5432
	hasPostgresPort := false
	firstPositive := 0
	for _, p := range a.Ports {
		if p == 5432 {
			hasPostgresPort = true
			break
		}
		if p > 0 && firstPositive == 0 {
			firstPositive = p
		}
	}
	if !hasPostgresPort && firstPositive > 0 {
		port = firstPositive
	}
	return domain.DBInfo{
		Type:        domain.DBTypePostgreSQL,
		Version:     postgresVersionFromImage(a.Image),
		Domain:      domainName,
		Name:        a.Name,
		Host:        a.Name,
		Port:        port,
		ContainerID: a.ContainerID,
		ImageName:   a.Image,
	}
}

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

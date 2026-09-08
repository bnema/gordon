package backup

// App-identity backup targets (04-network.md §8.3 preparation).
//
// The live DetectDatabases path resolves targets through domain-keyed
// attachments (containerSvc.ListAttachments). That coupling dies at
// cutover with the attachments family; backups must already resolve the
// same databases from the app ACTIVE record instead. This file is that
// adaptation, preparation form: pure detection over service descriptors
// plus app identity on the resulting DBInfo.
//
// Unwired until cutover: the service method that enumerates ACTIVE
// services per app and the backup naming/scheduling rewiring that
// prefers (app, service) over domain land with the handlers. The
// detection RULES are shared with the attachment path (same helpers),
// so parity is structural, not coincidental.

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
}

// DetectAppDatabases inspects app service descriptors and returns the
// detected databases. Rules mirror detectDatabaseFromAttachment; the
// resulting DBInfo carries App/Service identity and leaves Domain empty
// (domain-keyed callers are untouched until the cutover rewiring).
func DetectAppDatabases(sources []AppDatabaseSource) []domain.DBInfo {
	dbs := make([]domain.DBInfo, 0, len(sources))
	for _, source := range sources {
		if db, ok := detectDatabaseFromAppSource(source); ok {
			dbs = append(dbs, db)
		}
	}
	return dbs
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

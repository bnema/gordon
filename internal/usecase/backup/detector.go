package backup

import (
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// detectDatabaseFromAttachment maps a container attachment to DB metadata.
func detectDatabaseFromAttachment(domainName string, a domain.Attachment) (domain.DBInfo, bool) {
	image := strings.ToLower(a.Image)
	name := strings.ToLower(a.Name)

	switch {
	case strings.Contains(image, "postgres"), strings.Contains(name, "postgres"):
		return domain.DBInfo{
			Type:        domain.DBTypePostgreSQL,
			Domain:      domainName,
			Name:        a.Name,
			Host:        a.Name,
			Port:        5432,
			ContainerID: a.ContainerID,
			ImageName:   a.Image,
		}, true
	default:
		return domain.DBInfo{}, false
	}
}

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
		port := 5432
		if len(a.Ports) > 0 && a.Ports[0] > 0 {
			port = a.Ports[0]
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
		}, true
	default:
		return domain.DBInfo{}, false
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

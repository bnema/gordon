package scripts

import (
	"os"

	"gogs.bnema.dev/gordon-echo/pkg/templating"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func CreateYAMLFiles(topDomain, adminEmail string, logger *utils.Logger) error {
	// Setup the TXT renderer for traefik-compose.goyml
	rendererCompose, err := templating.GetTXTRenderer("traefik-compose.goyml", nil, logger)
	if err != nil {
		return err
	}

	// Render the traefik-compose.goyml with payload data
	composeContent, err := rendererCompose.TXTRender(map[string]string{
		"topdomain": topDomain,
	})
	if err != nil {
		return err
	}

	// Save the rendered content to a file
	err = os.WriteFile("traefik-compose.yml", []byte(composeContent), 0644)
	if err != nil {
		return err
	}

	// Setup the TXT renderer for traefik.goyml
	rendererTraefik, err := templating.GetTXTRenderer("traefik.goyml", nil, logger)
	if err != nil {
		return err
	}

	// Render the traefik.goyml with payload data
	traefikContent, err := rendererTraefik.TXTRender(map[string]string{
		"admin.email": adminEmail,
	})
	if err != nil {
		return err
	}

	// Save the rendered content to a file
	err = os.WriteFile("traefik/traefik.yml", []byte(traefikContent), 0644)
	if err != nil {
		return err
	}

	return nil
}

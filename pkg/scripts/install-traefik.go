package scripts

import (
	"errors"
	"fmt"
	"os"

	root "gogs.bnema.dev/gordon-echo"
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/pkg/templating"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func CreateYAMLFiles(topDomain, adminEmail string, logger *utils.Logger) error {
	path := app.GetBuildDir() + "/"
	modelFS := root.ModelFS
	if modelFS == nil {
		return errors.New("modelFS is nil")
	}

	// Check if directory traefik exists, if not create it
	if _, err := os.Stat(path + "traefik"); os.IsNotExist(err) {
		err = os.Mkdir(path+"traefik", 0755)
		if err != nil {
			return err
		}
	}

	// Setup the TXT renderer for traefik-compose.goyml
	rendererCompose, err := templating.GetTXTRenderer("traefik-compose.goyml", modelFS, logger)
	fmt.Println("rendererCompose=", rendererCompose)
	if err != nil {
		return err
	}

	// Render the traefik-compose.goyml with payload data
	composeContent, err := rendererCompose.TXTRender(map[string]string{
		"topdomain": topDomain,
	})
	fmt.Println("composeContent=", composeContent)
	if err != nil {
		return err
	}
	// Save the rendered content to a file at the env path
	err = os.WriteFile(path+"docker-compose.yml", []byte(composeContent), 0644)
	if err != nil {
		return err
	}

	// Setup the TXT renderer for traefik.goyml
	rendererTraefik, err := templating.GetTXTRenderer("traefik.goyml", modelFS, logger)
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

	// Save the rendered content to a file at the env path
	err = os.WriteFile(path+"traefik/traefik.yml", []byte(traefikContent), 0644)
	if err != nil {
		return err
	}

	return nil

}

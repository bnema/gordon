package scripts

import (
	"errors"
	"fmt"
	"os"

	root "gogs.bnema.dev/gordon-echo"
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/pkg/docker"
	"gogs.bnema.dev/gordon-echo/pkg/templating"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func ensureDirExists(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return os.Mkdir(dir, 0755)
	}
	return nil
}
func CreateYAMLFiles(topDomain, adminEmail string, logger *utils.Logger) error {
	path := app.GetBuildDir() + "/"
	modelFS := root.ModelFS
	if modelFS == nil {
		return errors.New("modelFS is nil")
	}

	// Check if directory traefik exists, if not create it
	if err := ensureDirExists(path + "traefik"); err != nil {
		return err
	}
	// Check if directory traefik/volume exists, if not create it
	if err := ensureDirExists(path + "traefik/volume"); err != nil {
		return err
	}

	// Setup the TXT renderer for traefik-compose.goyml
	rendererCompose, err := templating.GetTXTRenderer("traefik-compose.goyml", modelFS, logger)
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
	// Save the rendered content to a file at the env path
	err = os.WriteFile(path+"traefik/docker-compose.yml", []byte(composeContent), 0644)
	if err != nil {
		return err
	}

	// Setup the TXT renderer for traefik.goyml
	rendererTraefik, err := templating.GetTXTRenderer("traefik.goyml", modelFS, logger)
	if err != nil {
		return err
	}

	// Render the traefik.goyml with payload
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

	// Now that we have the YAML files, we can launch the containers
	// Create acme.json with permissions 600
	if f, err := os.Create(path + "traefik/volume/acme.json"); err != nil {
		return err
	} else if err = f.Chmod(0600); err != nil {
		return err
	} else {
		f.Close() // Schedule closing of the file when function exits
	}

	// Create the network "exposed" if it doesn't exist
	// Ensure that the network "exposed" exists if not create it
	exists, err := docker.NetworkExists("exposed")
	if err != nil {
		return err
	}
	if exists {
		fmt.Println("Network Exposed found")
	} else {
		fmt.Println("Network Exposed not found, creating it")
		if err := docker.CreateNetwork("exposed"); err != nil {
			return err
		}
	}

	fmt.Println("Network created successfully")

	// Create the containers with CreateContainerFromComposeFile
	if err := docker.CreateContainerFromComposeFile(path+"traefik/docker-compose.yml", logger); err != nil {
		return err
	}

	fmt.Println("Containers created successfully")

	return nil

}

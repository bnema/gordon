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

var path string

func init() {
	path = app.GetBuildDir() + "/"
}

func InstallTraefik(topDomain, adminEmail string, logger *utils.Logger) (bool, error) {
	// Create the YAML files
	if success, err := createYAMLFiles(topDomain, adminEmail, logger); err != nil {
		return false, err
	} else if !success {
		return false, errors.New("failed to create YAML files")
	}

	// Deploy the containers
	if success, err := deployTraefik(topDomain, adminEmail, logger); err != nil {
		return false, err
	} else if !success {
		return false, errors.New("failed to deploy Traefik")
	}

	return true, nil
}

func ensureDirExists(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return os.Mkdir(dir, 0755)
	}
	return nil
}

func createYAMLFiles(topDomain, adminEmail string, logger *utils.Logger) (bool, error) {
	modelFS := root.ModelFS
	if modelFS == nil {
		return false, errors.New("modelFS is nil")
	}

	// Check if directory traefik exists, if not create it
	if err := ensureDirExists(path + "traefik"); err != nil {
		return false, err
	}
	// Check if directory traefik/volume exists, if not create it
	if err := ensureDirExists(path + "traefik/volume"); err != nil {
		return false, err
	}

	// Setup the TXT renderer for traefik-compose.goyml
	rendererCompose, err := templating.GetTXTRenderer("traefik-compose.goyml", modelFS, logger)
	if err != nil {
		return false, err
	}

	// Render the traefik-compose.goyml with payload data
	composeContent, err := rendererCompose.TXTRender(map[string]string{
		"topdomain": topDomain,
	})
	if err != nil {
		return false, err
	}
	// Save the rendered content to a file at the env path
	err = os.WriteFile(path+"traefik/docker-compose.yml", []byte(composeContent), 0644)
	if err != nil {
		return false, err
	}

	// Setup the TXT renderer for traefik.goyml
	rendererTraefik, err := templating.GetTXTRenderer("traefik.goyml", modelFS, logger)
	if err != nil {
		return false, err
	}

	// Render the traefik.goyml with payload
	traefikContent, err := rendererTraefik.TXTRender(map[string]string{
		"admin.email": adminEmail,
	})
	if err != nil {
		return false, err
	}

	// Save the rendered content to a file at the env path
	err = os.WriteFile(path+"traefik/traefik.yml", []byte(traefikContent), 0644)
	if err != nil {
		return false, err
	}

	return true, nil

}

func deployTraefik(topDomain, adminEmail string, logger *utils.Logger) (bool, error) {
	// Now that we have the YAML files, we can launch the containers
	// Create acme.json with permissions 600
	if f, err := os.Create(path + "traefik/volume/acme.json"); err != nil {
		return false, err
	} else if err = f.Chmod(0600); err != nil {
		return false, err
	} else {
		f.Close() // Schedule closing of the file when function exits
	}

	// traefik.log
	if f, err := os.Create(path + "traefik/traefik.log"); err != nil {
		return false, err
	} else if err = f.Chmod(0600); err != nil {
		return false, err
	} else {
		f.Close() // Schedule closing of the file when function exits
	}

	// check if docker is running
	if s, err := docker.CheckDockerRunning(); err != nil {
		return false, err
	} else if s {
		fmt.Println("Docker is running")
	} else {
		fmt.Println("Docker is not running")
	}

	// check if a container named traefik already exists
	if exists, err := docker.ContainerExists("traefik"); err != nil {
		return false, err
	} else if exists {
		return false, errors.New("container traefik already exists")
	} else {
		fmt.Println("Container Traefik not found, creating it")
	}

	// check if there is already a container named traefik

	// Create the network "exposed" if it doesn't exist
	// Ensure that the network "exposed" exists if not create it
	exists, err := docker.NetworkExists("exposed")
	if err != nil {
		return false, err
	}
	if exists {
		fmt.Println("Network Exposed found")
	} else {
		fmt.Println("Network Exposed not found, creating it")
		if err := docker.CreateNetwork("exposed"); err != nil {
			return false, err
		}
	}

	fmt.Println("Network created successfully")

	// Create the containers with CreateContainerFromComposeFile (bool, error)
	succes, err := docker.CreateContainerFromComposeFile(path+"traefik/docker-compose.yml", logger)
	if err != nil {
		return false, err
	}
	if succes {
		fmt.Println("Containers created successfully")
		return true, nil
	}

	return false, errors.New("failed to create containers")
}

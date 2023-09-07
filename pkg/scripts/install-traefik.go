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

const (
	traefikPath       = "traefik"
	traefikVolumePath = "traefik/volume"
	traefikLog        = "logs/traefik.log"
	acmeJSON          = "traefik/volume/acme.json"
	networkName       = "totototo"
)

func init() {
	path = app.GetBuildDir() + "/"
}

type InstallationResult struct {
	Success bool
	Message string
	Err     error
}

func InstallTraefik(topDomain, adminEmail string, a *app.App) (bool, error) {
	// 1 - Files/directories creation
	// 1.1 - Check if directory traefik exists, if not create it
	if err := createDirectory(path + traefikPath); err != nil {
		a.AppLogger.Log(utils.ERROR, err)
		return false, err
	} else {
		fmt.Println("traefik directory created")
	}

	// 1.2 - Check if directory traefik/volume exists, if not create it
	if err := createDirectory(path + traefikVolumePath); err != nil {
		a.AppLogger.Log(utils.ERROR, err)
		return false, err
	} else {
		fmt.Println("traefik/volume directory created")
	}

	// 1.3 - touch traefik/traefik.log
	if err := createTraefikLogFile(path + traefikLog); err != nil {
		a.AppLogger.Log(utils.ERROR, err)
		return false, err
	} else {
		a.AppLogger.Log(utils.INFO, "Traefik log file created")
	}

	// 1.4 - touch traefik/volume/acme.json
	if err := createACMEfile(path + acmeJSON); err != nil {
		a.AppLogger.Log(utils.ERROR, err)
		return false, err
	}

	a.AppLogger.Log(utils.INFO, "Traefik acme.json file created")

	// 2 - Render the yml files
	// 2.1 - Render traefik-compose.yml
	traefikComposeContent, err := renderTemplate("traefik-compose.goyml", topDomain, adminEmail, a.AppLogger)
	if err != nil {
		a.AppLogger.Log(utils.ERROR, err)
		return false, err
	}

	a.AppLogger.Log(utils.INFO, "Traefik compose file rendered")

	// 2.2 - Save traefik-compose.yml
	if err := saveFile(path+"docker-compose.yml", traefikComposeContent); err != nil {
		a.AppLogger.Log(utils.ERROR, err)
		return false, err
	} else {
		fmt.Println("docker-compose.yml saved")
	}

	// 2.3 - Render traefik.yml
	traefikContent, err := renderTemplate("traefik.goyml", topDomain, adminEmail, a.AppLogger)
	if err != nil {
		a.AppLogger.Log(utils.ERROR, err)
		return false, err
	}

	a.AppLogger.Log(utils.INFO, "Traefik file rendered")

	// 2.4 - Save traefik.yml
	if err := saveFile(path+"traefik.yml", traefikContent); err != nil {
		a.AppLogger.Log(utils.ERROR, err)
		return false, err
	}

	a.AppLogger.Log(utils.INFO, "Traefik file saved")

	// 3 - Check if traefik is running
	if err := checkIfTraefikIsRunning(a); err != nil {
		a.AppLogger.Log(utils.ERROR, err)
		return false, err
	}

	// // 4 - Create network
	// if err := docker.CreateNetwork(networkName, a); err != nil {
	// 	a.AppLogger.Log(utils.ERROR, err)
	// 	return false, err
	// }

	// 5 - Create traefik container
	if err := createTraefikContainer(path+"docker-compose.yml", a); err != nil {
		a.AppLogger.Log(utils.ERROR, err)
		return false, err
	}

	// 6 - Ping traefik
	if err := pingWithCURL(topDomain); err != nil {
		a.AppLogger.Log(utils.ERROR, err)
		return false, err
	}

	return true, nil
}

func renderTemplate(filename, topDomain, adminEmail string, logger *utils.Logger) (string, error) {
	modelFS := root.ModelFS
	if modelFS == nil {
		return "", errors.New("modelFS is nil")
	}

	renderer, err := templating.GetTXTRenderer(filename, modelFS, logger)
	if err != nil {
		return "", err
	}

	var contentData map[string]interface{}
	switch filename {
	case "traefik-compose.goyml":
		contentData = map[string]interface{}{"topdomain": topDomain}
	case "traefik.goyml":
		contentData = map[string]interface{}{
			"admin": map[string]string{
				"email": adminEmail,
			},
		}
	default:
		return "", errors.New("invalid template filename")
	}

	return renderer.TXTRender(contentData)
}

func createDirectory(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return os.Mkdir(dir, 0755)
	}
	return nil
}
func saveFile(filePath, content string) error {
	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	if err != nil {
		fmt.Println("Error while writing to file: ", err)
		return err
	}
	return err
}

func closeFile(f *os.File) error {
	// defer f.Close()
	return f.Close()
}

func createACMEfile(filePath string) error {
	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Chmod(0600)
}

func createTraefikLogFile(filePath string) error {
	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Chmod(0600)
}

func checkIfTraefikIsRunning(a *app.App) error {
	// Check if docker is running
	if err := docker.CheckIfDockerIsRunning(a); err != nil {
		return err
	}
	// Check if a container named traefik already exists
	if err := docker.ContainerExists("traefik", a); err != nil {
		return err
	}

	return nil
}

func createTraefikContainer(filePath string, a *app.App) error {
	err := docker.CreateContainerFromComposeFile(filePath, a)
	if err != nil {
		return err
	}

	return nil
}

func pingWithCURL(topDomain string) error {
	return nil
}

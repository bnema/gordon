package docker

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	"gogs.bnema.dev/gordon-echo/internal/app"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

// Use the App struct to access the logger

func CreateImageFromDockerfile(a *app.App) error {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		a.AppLogger.Log(utils.ERROR, fmt.Sprintf("Failed to initialize Docker client: %v", err))
		defer cli.Close()
	}

	a.AppLogger.Log(utils.INFO, "Building image...")
	buildCtx, err := archive.TarWithOptions("tmp/docker-test/", &archive.TarOptions{})
	if err != nil {
		a.AppLogger.Log(utils.ERROR, fmt.Sprintf("Failed to create build context: %v", err))
	}
	defer buildCtx.Close()

	a.AppLogger.Log(utils.INFO, "Building image...")
	imageBuildResponse, err := cli.ImageBuild(ctx, buildCtx, types.ImageBuildOptions{
		Tags: []string{"gordon-echo:latest"},
	})
	if err != nil {
		a.AppLogger.Log(utils.ERROR, fmt.Sprintf("Failed to build image: %v", err))
	}
	defer imageBuildResponse.Body.Close()

	a.AppLogger.Log(utils.INFO, "Saving image to file...")

	imageTar, err := cli.ImageSave(ctx, []string{"gordon-echo:latest"})
	if err != nil {
		a.AppLogger.Log(utils.ERROR, fmt.Sprintf("Failed to save image: %v", err))
	}

	outputFile, err := os.Create("tmp/docker-test/gordon-echo.tar")
	if err != nil {
		a.AppLogger.Log(utils.ERROR, fmt.Sprintf("Failed to create output file: %v", err))
	}
	defer outputFile.Close()

	_, err = io.Copy(outputFile, imageTar)
	if err != nil {
		a.AppLogger.Log(utils.ERROR, fmt.Sprintf("Failed to save image to file: %v", err))
	}

	return nil
}

func CreateNetwork(name string, a *app.App) error {
	a.AppLogger.Log(utils.INFO, "Entering CreateNetwork function with name: "+name)

	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		errMsg := fmt.Sprintf("Failed to initialize Docker client: %v", err)
		a.AppLogger.Log(utils.ERROR, errMsg)
		return errors.New(errMsg)
	}
	defer cli.Close()

	a.AppLogger.Log(utils.INFO, "Attempting to create network...")
	_, err = cli.NetworkCreate(ctx, name, types.NetworkCreate{
		CheckDuplicate: true,
		Driver:         "bridge",
	})
	if err != nil {
		errMsg := fmt.Sprintf("Failed to create network: %v", err)
		a.AppLogger.Log(utils.ERROR, errMsg)
		return errors.New(errMsg)
	}

	a.AppLogger.Log(utils.INFO, "Successfully created network: "+name)
	return nil
}

// Check if a container exists
func ContainerExists(name string, a *app.App) error {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	defer cli.Close()

	fmt.Println("Checking if container exists...")
	containers, err := cli.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		return err
	}

	for _, container := range containers {
		for _, containerName := range container.Names {
			if containerName == "/"+name {
				return errors.New("container already exists")
			}
		}
	}

	return nil
}

func CheckIfDockerIsRunning(a *app.App) error {
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err != nil {
		return errors.New("docker is not running")
	}
	return nil
}

// CreateContainerFromComposeFile creates containers using docker compose based on the provided compose file.
func CreateContainerFromComposeFile(composeFilePath string, a *app.App) error {
	a.AppLogger.Log(utils.INFO, "Executing CreateContainerFromComposeFile()")

	// Extract directory from the composeFilePath
	dir := filepath.Dir(composeFilePath)

	// Change directory to the compose file directory
	if err := os.Chdir(dir); err != nil {
		return errors.New("failed to change directory: " + err.Error())
	}

	// Create the command
	cmd := exec.Command("docker", "compose", "up", "-d")

	// Capture standard output and error
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Execute the command
	if err := cmd.Run(); err != nil {
		// Log the captured standard output and error for better diagnostics
		a.AppLogger.Log(utils.ERROR, "Docker Compose Output: "+stdout.String())
		a.AppLogger.Log(utils.ERROR, "Docker Compose Error: "+stderr.String())
		return errors.New("failed to execute docker compose command: " + err.Error())
	}

	return nil
}

func streamToLogger(r io.Reader, logger *utils.Logger, level utils.LogLevel) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		logger.Log(level, scanner.Text())
	}
}

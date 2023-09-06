package docker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/stdcopy"
	"gogs.bnema.dev/gordon-echo/pkg/utils"
)

func DockertPullImageTest() {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}
	defer cli.Close()

	reader, err := cli.ImagePull(ctx, "docker.io/library/alpine", types.ImagePullOptions{})
	if err != nil {
		panic(err)
	}
	io.Copy(os.Stdout, reader)

	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: "alpine",
		Cmd:   []string{"echo", "hello world"},
	}, nil, nil, nil, "")
	if err != nil {
		panic(err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		panic(err)
	}

	statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			panic(err)
		}
	case <-statusCh:
	}

	out, err := cli.ContainerLogs(ctx, resp.ID, types.ContainerLogsOptions{ShowStdout: true})
	if err != nil {
		panic(err)
	}

	stdcopy.StdCopy(os.Stdout, os.Stderr, out)
}

func DockerListContainersTest() {
	fmt.Println("Executing DockerListContainersTest()")

	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}
	defer cli.Close()

	fmt.Println("Initialized Docker client.")

	containers, err := cli.ContainerList(ctx, types.ContainerListOptions{All: true})
	if err != nil {
		panic(err)
	}

	fmt.Printf("Found %d containers.\n", len(containers))

	for _, container := range containers {
		fmt.Printf("%s %s\n", container.ID[:10], container.Status)
	}
}

// I have a Dockerfile inside tmp/docker-test/Dockerfile and i want to create an image from it and place it at the same directory.
func CreateImageFromDockerfile() {
	fmt.Println("Executing CreateImageFromDockerfile()")
	fmt.Println("Initialized Docker client.")
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}
	defer cli.Close()

	fmt.Println("Building image...")
	buildCtx, err := archive.TarWithOptions("tmp/docker-test/", &archive.TarOptions{})
	if err != nil {
		panic(err)
	}
	defer buildCtx.Close()

	fmt.Println("Building image...")
	imageBuildResponse, err := cli.ImageBuild(ctx, buildCtx, types.ImageBuildOptions{
		Tags: []string{"gordon-echo:latest"},
	})
	if err != nil {
		panic(err)
	}
	defer imageBuildResponse.Body.Close()

	fmt.Println("Saving image to file...")

	imageTar, err := cli.ImageSave(ctx, []string{"gordon-echo:latest"})
	if err != nil {
		panic(err)
	}

	outputFile, err := os.Create("tmp/docker-test/gordon-echo.tar")
	if err != nil {
		panic(err)
	}
	defer outputFile.Close()

	_, err = io.Copy(outputFile, imageTar)
	if err != nil {
		panic(err)
	}
}

// Check if a network exists
func NetworkExists(name string) (bool, error) {
	fmt.Println("Initialized Docker client.")
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}
	defer cli.Close()

	fmt.Println("Checking if network exists...")
	networks, err := cli.NetworkList(ctx, types.NetworkListOptions{})
	if err != nil {
		panic(err)
	}

	for _, network := range networks {
		if network.Name == name {
			return true, nil
		}
	}

	return false, nil
}

// Create network as a bridge
func CreateNetwork(name string) error {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}
	defer cli.Close()

	fmt.Println("Creating network...")
	_, err = cli.NetworkCreate(ctx, name, types.NetworkCreate{
		Driver: "bridge",
	})
	if err != nil {
		panic(err)
	}

	return nil
}

// CreateContainerFromComposeFile creates containers using docker compose based on the provided compose file.
func CreateContainerFromComposeFile(composeFilePath string, logger *utils.Logger) error {
	fmt.Println("Executing CreateContainerFromComposeFile()")

	// Create and start containers using docker compose in the foreground
	cmd := exec.Command("docker", "compose", "-f", composeFilePath, "up")

	// Get stdout and stderr pipes
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe: %v", err)
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start docker compose: %v", err)
	}

	// Stream stdout to logger with INFO level
	go streamToLogger(stdout, logger, utils.INFO)
	// Stream stderr to logger with ERROR level
	go streamToLogger(stderr, logger, utils.ERROR)

	// Wait for the command to finish
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("docker compose command finished with error: %v", err)
	}

	fmt.Println("Containers started successfully using docker compose")
	return nil
}

func streamToLogger(r io.Reader, logger *utils.Logger, level utils.LogLevel) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		logger.Log(level, scanner.Text())
	}
}

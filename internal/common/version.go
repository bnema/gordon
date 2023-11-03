package common

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/bnema/gordon/pkg/docker"
)

type CurrentVersion struct {
	isDocker bool
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Date     string `json:"date"`
	Digest   string `json:"digest"`
}

// {
// "amd64": {
// "name": "0.0.951-amd64",
// "digest": "sha256:8486058cea46c5a18bc73d19446d6590b8b49cfadd7128ea443abbd69c11ea83"
// },
// "arm64": {
// "name": "0.0.951-arm64",
// "digest": "sha256:d9c2749117711e0bccd9cf3a3ac36ef7c4aa5487046e584640f2467add57a829"
// }
// }
// VersionInfo represents the JSON structure of the version endpoint response.

// ArchVersion represents the architecture-specific version details.
type ArchVersion struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type VersionInfo struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type VersionsResponse struct {
	AMD64 VersionInfo `json:"amd64"`
	ARM64 VersionInfo `json:"arm64"`
}

func GetCurrentBuildVersionInfo(c *Config) CurrentVersion {
	// Check if the Docker client has been initialized .iscontainer should be at root level
	isContainer, err := docker.IsRunningInContainer()
	if err != nil {
		log.Println(err)
	}

	digest, err := docker.WhoAmI()
	if err != nil {
		log.Println(err)
	}

	return CurrentVersion{
		isDocker: isContainer,
		Version:  c.Build.BuildVersion,
		Commit:   c.Build.BuildCommit,
		Date:     c.Build.BuildDate,
		Digest:   digest,
	}
}

// CheckForNewVersion compares the local and remote version.
// If the remote version is newer, it returns a message stating that a new version is available.
func CheckForNewVersion(local CurrentVersion, remote VersionsResponse) string {
	arch := getArch()

	var remoteVersionInfo VersionInfo
	switch arch {
	case "amd64":
		remoteVersionInfo = remote.AMD64
	case "arm64":
		remoteVersionInfo = remote.ARM64
	default:
		log.Printf("Unknown architecture: %s", arch)
		return ""
	}

	localVersion := normalizeVersion(local.Version, arch)
	remoteVersion := normalizeVersion(remoteVersionInfo.Name, arch)

	// Compare the normalized local version with the remote version
	if localVersion != remoteVersion {
		return "a new version is available"
	}
	return ""
}

// normalizeVersion ensures that the version strings are comparable.
func normalizeVersion(version, arch string) string {
	// Assuming the version string might not contain the architecture.
	if !strings.Contains(version, arch) {
		version = fmt.Sprintf("%s-%s", version, arch)
	}
	return version
}

func getArch() string {
	return runtime.GOARCH
}

func CheckVersionPeriodically(c *Config) (string, error) {
	timer := time.NewTicker(1 * time.Minute)
	defer timer.Stop()

	for range timer.C {
		localVersion := getLocalVersion(c)
		remoteVersion, err := getRemoteVersion(c)
		if err != nil {
			log.Printf("Error fetching remote version: %s", err)
			continue
		}

		message := CheckForNewVersion(localVersion, remoteVersion)
		if message != "" {
			log.Printf("New version available: %s", message)
			// TODO: Add a notification
		}
	}

	return "", nil
}

func getLocalVersion(c *Config) CurrentVersion {
	return CurrentVersion{
		Version: c.Build.BuildVersion,
		Commit:  c.Build.BuildCommit,
		Date:    c.Build.BuildDate,
	}
}

func getRemoteVersion(c *Config) (VersionsResponse, error) {
	proxyURL := c.Build.ProxyURL
	if proxyURL == "" {
		return VersionsResponse{}, fmt.Errorf("proxy URL is empty")
	}

	checkVersionEndpoint := proxyURL + "/version"
	resp, err := http.Get(checkVersionEndpoint)
	if err != nil {
		return VersionsResponse{}, err
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return VersionsResponse{}, err
	}

	var remoteVersionInfo VersionsResponse
	err = json.Unmarshal(body, &remoteVersionInfo)
	if err != nil {
		return VersionsResponse{}, err
	}

	return remoteVersionInfo, nil
}

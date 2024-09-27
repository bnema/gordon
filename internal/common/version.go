package common

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"time"

	"github.com/bnema/gordon/pkg/docker"
	"github.com/charmbracelet/log"
)

type CurrentVersion struct {
	isContainer bool
	Version     string `json:"version"`
}

type ArchVersion struct {
	Name string `json:"name"`
}

type VersionInfo struct {
	Name string `json:"name"`
}

type VersionsResponse struct {
	AMD64 string `json:"amd64"`
	ARM64 string `json:"arm64"`
}

func GetCurrentBuildVersionInfo(c *Config) CurrentVersion {
	return CurrentVersion{
		isContainer: docker.IsRunningInContainer(),
		Version:     c.GetVersion(),
	}
}

func CheckForNewVersion(localVersion, remoteVersion string) string {
	if localVersion != remoteVersion {
		return "A new version is available"
	}
	return ""
}

func getArch() string {
	return runtime.GOARCH
}

func checkAndUpdateVersion(c *Config) (string, error) {
	localVersion := GetCurrentBuildVersionInfo(c)
	remoteVersions, err := getRemoteVersion(c)
	if err != nil {
		return "", err
	}

	var remoteVersion string
	switch getArch() {
	case "amd64":
		remoteVersion = remoteVersions.AMD64
	case "arm64":
		remoteVersion = remoteVersions.ARM64
	default:
		return "", fmt.Errorf("unsupported architecture: %s", getArch())
	}

	// Check if remoteVersion is empty
	if remoteVersion == "" {
		return "", fmt.Errorf("remote version not available for architecture %s", getArch())
	}

	message := CheckForNewVersion(localVersion.Version, remoteVersion)

	// Return the message without an error, even if it's empty
	return message, nil
}

// GetVersion returns the version
func (c *Config) GetVersion() string {
	if c.Build.BuildVersion != "" {
		return c.Build.BuildVersion
	}
	return "devel"
}

func CheckVersionPeriodically(c *Config) (string, error) {
	// Check if version is "devel"
	if c.GetVersion() == "devel" {
		return "", nil
	}

	// Perform an immediate check.
	newVersionMessage, err := checkAndUpdateVersion(c)

	if err != nil {
		return "", err
	}

	if newVersionMessage != "" {
		log.Info(newVersionMessage)
		return "", nil
	}

	// Set up a ticker to check every 5 minutes.
	timer := time.NewTicker(5 * time.Minute)
	defer timer.Stop()

	for range timer.C {
		newVersionMessage, err = checkAndUpdateVersion(c)
		if err != nil {
			log.Printf("Error fetching remote version: %s", err)
			continue
		}

		if newVersionMessage != "" {
			fmt.Println(newVersionMessage)
			return "", nil //We return so the loop stops in case of a new version
		}
	}

	return "", nil
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

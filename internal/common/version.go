package common

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"time"

	"github.com/bnema/gordon/pkg/docker"
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
	Name  string      `json:"name"`
	AMD64 VersionInfo `json:"amd64"`
	ARM64 VersionInfo `json:"arm64"`
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
		remoteVersion = remoteVersions.AMD64.Name
	case "arm64":
		remoteVersion = remoteVersions.ARM64.Name
	default:
		remoteVersion = remoteVersions.Name
	}

	message := CheckForNewVersion(localVersion.Version, remoteVersion)
	if message != "" {
		return fmt.Sprintf("New version %s is available", remoteVersion), nil
	}
	return "", nil
}

func CheckVersionPeriodically(c *Config) (string, error) {
	// Perform an immediate check.
	newVersionMessage, err := checkAndUpdateVersion(c)

	if err != nil {
		return "", err
	}

	if newVersionMessage != "" {
		fmt.Println(newVersionMessage)
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

// func getLocalVersion(c *Config) CurrentVersion {
// 	// Dummy implementation, replace with your actual logic
// 	return CurrentVersion{Version: "0.0.970"} // Example version
// }

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

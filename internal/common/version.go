// internal/common/version.go

package common

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/bnema/gordon/pkg/logger"
)

// Info represents version information
type Info struct {
	Version  string
	Commit   string
	Date     string
	ProxyURL string
}

type ProxyVersion struct {
	Version string `json:"version"`
}

// GetVersionInfo returns the current version information based on BuildConfig
func GetVersionInfo(buildVersion, buildCommit, buildDate, proxyURL string) Info {
	// If version was set in BuildConfig, use that
	if buildVersion != "" && buildVersion != "dev" {
		return Info{
			Version:  buildVersion,
			Commit:   buildCommit,
			Date:     buildDate,
			ProxyURL: proxyURL,
		}
	}

	// Try to get version from runtime build info (for go install)
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		return Info{
			Version:  buildInfo.Main.Version,
			Commit:   "unknown",
			Date:     "unknown",
			ProxyURL: proxyURL,
		}
	}

	// Fallback for development builds
	return Info{
		Version:  "dev",
		Commit:   "unknown",
		Date:     "unknown",
		ProxyURL: proxyURL,
	}
}

// CheckForNewVersion checks if a newer version is available via proxy
func CheckForNewVersion(currentVersion, proxyURL string) (bool, string, error) {
	// Don't check for new versions in development builds or dirty builds
	if currentVersion == "dev" || strings.Contains(currentVersion, "-dirty") {
		return false, "", nil
	}

	// If currentVersion is empty, treat it as v0.0.0
	if currentVersion == "" {
		currentVersion = "v0.0.0"
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get(proxyURL + "/version")
	if err != nil {
		return false, "", fmt.Errorf("error making request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, "", fmt.Errorf("error reading response: %w", err)
	}

	var proxyVer ProxyVersion
	if err := json.Unmarshal(body, &proxyVer); err != nil {
		return false, "", fmt.Errorf("error parsing JSON: %w", err)
	}

	// Ensure versions start with 'v'
	if !strings.HasPrefix(currentVersion, "v") {
		currentVersion = "v" + currentVersion
	}
	if !strings.HasPrefix(proxyVer.Version, "v") {
		proxyVer.Version = "v" + proxyVer.Version
	}

	// Clean up version strings
	latestVersion := strings.TrimPrefix(proxyVer.Version, "v")
	currentBase := extractBaseVersion(currentVersion)

	// Handle development versions
	if currentBase == "" || currentBase == "dev" {
		currentBase = "0.0.0"
	}

	// Parse versions using semver
	current, err := semver.NewVersion(currentBase)
	if err != nil {
		return false, "", fmt.Errorf("error parsing current version (%s): %w", currentBase, err)
	}

	latest, err := semver.NewVersion(latestVersion)
	if err != nil {
		return false, "", fmt.Errorf("error parsing latest version (%s): %w", latestVersion, err)
	}

	if latest.GreaterThan(current) {
		return true, proxyVer.Version, nil
	}

	return false, "", nil
}

// CheckVersionPeriodically checks for new versions periodically
func CheckVersionPeriodically(info Info, checkInterval time.Duration) {
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	// Initial check
	checkVersion(info)

	// Periodic checks
	for range ticker.C {
		checkVersion(info)
	}
}

func checkVersion(info Info) {
	hasUpdate, latestVersion, err := CheckForNewVersion(info.Version, info.ProxyURL)
	if err != nil {
		logger.Error("Failed to check for updates", "error", err)
		return
	}

	if hasUpdate {
		logger.Info("New version available!",
			"current", info.Version,
			"latest", latestVersion,
			"update_url", "https://github.com/bnema/gordon/releases/latest")
	}
}

func extractBaseVersion(version string) string {
	// Remove 'v' prefix if present
	version = strings.TrimPrefix(version, "v")

	// Handle empty or invalid versions
	if version == "" || version == "dev" {
		return "0.0.0"
	}

	// Split on first hyphen to remove dirty/commit info
	parts := strings.SplitN(version, "-", 2)
	return parts[0]
}

// String returns a formatted version string
func (i Info) String() string {
	if i.Version == "dev" {
		return "Gordon version dev (development build)"
	}

	// Check if it's a dirty version
	if strings.Contains(i.Version, "-dirty") {
		return fmt.Sprintf("Gordon version %s (commit: %s, built at: %s)",
			i.Version, i.Commit, i.Date)
	}

	return fmt.Sprintf("Gordon version %s (commit: %s, built at: %s)",
		i.Version, i.Commit, i.Date)
}

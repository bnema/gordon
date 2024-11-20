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
	"github.com/charmbracelet/log"
)

const (
	githubAPI = "https://api.github.com/repos/bnema/gordon/releases/latest"
)

// Info represents version information
type Info struct {
	Version string
	Commit  string
	Date    string
}

// GitHubRelease represents the GitHub API response
type GitHubRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
}

// GetVersionInfo returns the current version information based on BuildConfig
func GetVersionInfo(buildVersion, buildCommit, buildDate string) Info {
	// If version was set in BuildConfig, use that
	if buildVersion != "" && buildVersion != "dev" {
		return Info{
			Version: buildVersion,
			Commit:  buildCommit,
			Date:    buildDate,
		}
	}

	// Try to get version from runtime build info (for go install)
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		return Info{
			Version: buildInfo.Main.Version,
			Commit:  "unknown",
			Date:    "unknown",
		}
	}

	// Fallback for development builds
	return Info{
		Version: "dev",
		Commit:  "unknown",
		Date:    "unknown",
	}
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

// CheckForNewVersion checks if a newer version is available on GitHub
func CheckForNewVersion(currentVersion string) (bool, string, error) {
	// Don't check for new versions in development builds or dirty builds
	if currentVersion == "dev" || strings.Contains(currentVersion, "-dirty") {
		return false, "", nil
	}

	// Create an HTTP client with timeout
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	// Create request with User-Agent header (GitHub API requires this)
	req, err := http.NewRequest("GET", githubAPI, nil)
	if err != nil {
		return false, "", fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set("User-Agent", "Gordon-Version-Checker")

	// Make the request
	resp, err := client.Do(req)
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

	var release GitHubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return false, "", fmt.Errorf("error parsing JSON: %w", err)
	}

	// Clean up version strings
	latestVersion := strings.TrimPrefix(release.TagName, "v")
	currentBase := extractBaseVersion(currentVersion)

	// Parse versions using semver
	current, err := semver.NewVersion(currentBase)
	if err != nil {
		return false, "", fmt.Errorf("error parsing current version: %w", err)
	}

	latest, err := semver.NewVersion(latestVersion)
	if err != nil {
		return false, "", fmt.Errorf("error parsing latest version: %w", err)
	}

	// Compare versions
	if latest.GreaterThan(current) {
		return true, release.TagName, nil
	}

	return false, "", nil
}

// CheckVersionPeriodically checks for new versions periodically
func CheckVersionPeriodically(currentVersion string, checkInterval time.Duration) {
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	// Initial check
	checkVersion(currentVersion)

	// Periodic checks
	for range ticker.C {
		checkVersion(currentVersion)
	}
}

func checkVersion(currentVersion string) {
	hasUpdate, latestVersion, err := CheckForNewVersion(currentVersion)
	if err != nil {
		log.Error("Failed to check for updates", "error", err)
		return
	}

	if hasUpdate {
		log.Info("New version available!",
			"current", currentVersion,
			"latest", latestVersion,
			"update_url", "https://github.com/bnema/gordon/releases/latest")
	}
}

func extractBaseVersion(version string) string {
	// Remove 'v' prefix if present
	version = strings.TrimPrefix(version, "v")

	// Split on first hyphen to remove dirty/commit info
	parts := strings.SplitN(version, "-", 2)
	return parts[0]
}

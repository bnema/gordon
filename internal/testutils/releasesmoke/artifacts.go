package releasesmoke

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

type artifactEntry struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

var dockerImageNamePattern = regexp.MustCompile(`^ghcr\.io/bnema/gordon:v[^:]*-`)

// ImageForArch reads dist/artifacts.json and returns the single Docker image name for arch.
func ImageForArch(artifactsPath, arch string) (string, error) {
	data, err := os.ReadFile(artifactsPath)
	if err != nil {
		return "", fmt.Errorf("read artifacts.json: %w", err)
	}
	var entries []artifactEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return "", fmt.Errorf("decode artifacts.json: %w", err)
	}
	suffix := "-" + arch
	pattern := regexp.MustCompile(dockerImageNamePattern.String() + regexp.QuoteMeta(arch) + `$`)
	var matches []string
	for _, entry := range entries {
		if entry.Type != "Docker Image" {
			continue
		}
		if pattern.MatchString(entry.Name) {
			matches = append(matches, entry.Name)
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected exactly one Docker Image for %s (suffix %q), found %d", arch, suffix, len(matches))
	}
	return matches[0], nil
}

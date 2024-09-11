// This upload feature is based on pocketbase's ghupdate command.
// Thank you for your work, @Gani Georgiev
// Check the original code here: https://github.com/pocketbase/pocketbase/blob/master/plugins/ghupdate/ghupdate.go
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/fatih/color"
	"github.com/pocketbase/pocketbase/tools/archive"
	"github.com/spf13/cobra"
)

type HttpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Config struct {
	Owner             string
	Repo              string
	ArchiveExecutable string
	Context           context.Context
	HttpClient        HttpClient
}

var pluginInstance *plugin

type plugin struct {
	app            *cli.App
	currentVersion string
	config         Config
}

func NewUpdateCommand(a *cli.App) *cobra.Command {

	pluginInstance = &plugin{
		app:            a,
		currentVersion: a.Config.Build.BuildVersion,
		config: Config{
			Owner:             "bnema",
			Repo:              "gordon",
			ArchiveExecutable: "gordon",
			Context:           context.Background(),
			HttpClient:        http.DefaultClient,
		},
	}

	if pluginInstance.app != a {
		panic("app instance is not the same")
	}

	if a == nil {
		panic("app instance is nil")
	}

	command := &cobra.Command{
		Use:           "update",
		Short:         "Update the Gordon executable",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(command *cobra.Command, args []string) error {
			var needConfirm bool
			if docker.IsRunningInContainer() {
				needConfirm = true
				color.Yellow("NB! It seems that you are in a Docker container.")
				color.Yellow("The update command may not work as expected in this context because usually the version of the app is managed by the container image itself.")
			}

			if needConfirm {
				confirm := false
				prompt := &survey.Confirm{
					Message: "Do you want to proceed with the update?",
				}
				survey.AskOne(prompt, &confirm)
				if !confirm {
					fmt.Println("The command has been cancelled.")
					return nil
				}
			}

			return pluginInstance.update()
		},
	}
	return command
}

func (p *plugin) update() error {
	color.Yellow("Fetching release information...")

	latest, err := fetchLatestRelease(
		p.config.Context,
		p.config.HttpClient,
		p.config.Owner,
		p.config.Repo,
	)
	if err != nil {
		return err
	}

	if compareVersions(strings.TrimPrefix(p.currentVersion, "v"), strings.TrimPrefix(latest.Tag, "v")) <= 0 {
		color.Green("You already have the latest version %s.", p.currentVersion)
		return nil
	}

	suffix := archiveSuffix(runtime.GOOS, runtime.GOARCH)
	if suffix == "" {
		return errors.New("unsupported platform")
	}

	asset, err := latest.findAssetBySuffix(suffix)
	if err != nil {
		return err
	}

	// temporary file is under common.Config.General.StorageDir
	releaseDir := filepath.Join(p.app.Config.General.StorageDir, "update")
	defer os.RemoveAll(releaseDir)

	color.Yellow("Downloading %s...", asset.Name)

	// download the release asset
	assetZip := filepath.Join(releaseDir, asset.Name)
	if err := downloadFile(p.config.Context, p.config.HttpClient, asset.DownloadUrl, assetZip); err != nil {
		return err
	}

	color.Yellow("Extracting %s...", asset.Name)

	extractDir := filepath.Join(releaseDir, "extracted_"+asset.Name)
	defer os.RemoveAll(extractDir)

	if err := archive.Extract(assetZip, extractDir); err != nil {
		return err
	}

	color.Yellow("Replacing the executable...")

	oldExec, err := os.Executable()
	if err != nil {
		return err
	}
	renamedOldExec := oldExec + ".old"
	defer os.Remove(renamedOldExec)

	newExec := filepath.Join(extractDir, p.config.ArchiveExecutable)
	if _, err := os.Stat(newExec); err != nil {
		// try again with an .exe extension
		newExec = newExec + ".exe"
		if _, fallbackErr := os.Stat(newExec); fallbackErr != nil {
			return fmt.Errorf("The executable in the extracted path is missing or it is inaccessible: %v, %v", err, fallbackErr)
		}
	}

	// rename the current executable
	if err := os.Rename(oldExec, renamedOldExec); err != nil {
		return fmt.Errorf("Failed to rename the current executable: %w", err)
	}

	tryToRevertExecChanges := func() {
		if revertErr := os.Rename(renamedOldExec, oldExec); revertErr != nil {
			log.Println(revertErr)
		}
	}

	// replace with the extracted binary
	if err := os.Rename(newExec, oldExec); err != nil {
		tryToRevertExecChanges()
		return fmt.Errorf("Failed replacing the executable: %w", err)
	}

	color.HiBlack("---")
	color.Green("Update completed successfully! You can start the executable as usual.")

	return nil
}

func (r *release) findAssetBySuffix(suffix string) (*asset, error) {
	for _, asset := range r.Assets {
		if strings.HasSuffix(asset.Name, suffix) {
			return &asset, nil
		}
	}

	return nil, fmt.Errorf("no asset found with suffix %q", suffix)
}

type release struct {
	Tag    string  `json:"tag_name"`
	Assets []asset `json:"assets"`
}

type asset struct {
	Name        string `json:"name"`
	DownloadUrl string `json:"browser_download_url"`
}

func fetchLatestRelease(
	ctx context.Context,
	client HttpClient,
	owner string,
	repo string,
) (*release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	rawBody, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	// http.Client doesn't treat non 2xx responses as error
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf(
			"(%d) failed to fetch latest releases:\n%s",
			res.StatusCode,
			string(rawBody),
		)
	}

	result := &release{}
	if err := json.Unmarshal(rawBody, result); err != nil {
		return nil, err
	}

	return result, nil
}

func downloadFile(
	ctx context.Context,
	client HttpClient,
	url string,
	destPath string,
) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	// http.Client doesn't treat non 2xx responses as error
	if res.StatusCode >= 400 {
		return fmt.Errorf("(%d) failed to send download file request", res.StatusCode)
	}

	// ensure that the dest parent dir(s) exist
	if err := os.MkdirAll(filepath.Dir(destPath), os.ModePerm); err != nil {
		return err
	}

	dest, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer dest.Close()

	if _, err := io.Copy(dest, res.Body); err != nil {
		return err
	}

	return nil
}

// archives:
//   - format: zip
//     name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"

func archiveSuffix(goos, goarch string) string {
	// Define a map to hold the combinations of goos and goarch.
	var archMap = map[string]map[string]string{
		"linux": {
			"amd64": "_linux_amd64.zip",
			"arm64": "_linux_arm64.zip",
			"arm":   "_linux_armv7.zip",
		},
		"darwin": {
			"amd64": "_darwin_amd64.zip",
			"arm64": "_darwin_arm64.zip",
		},
		"windows": {
			"amd64": "_windows_amd64.zip",
			"arm64": "_windows_arm64.zip",
		},
	}

	// Look up goos then goarch.
	if arch, ok := archMap[goos]; ok {
		if suffix, ok := arch[goarch]; ok {
			return suffix
		}
	}

	// Return a default value if no match is found.
	return ""
}
func compareVersions(a, b string) int {
	aSplit := strings.Split(a, ".")
	aTotal := len(aSplit)

	bSplit := strings.Split(b, ".")
	bTotal := len(bSplit)

	limit := aTotal
	if bTotal > aTotal {
		limit = bTotal
	}

	for i := 0; i < limit; i++ {
		var x, y int

		if i < aTotal {
			x, _ = strconv.Atoi(aSplit[i])
		}

		if i < bTotal {
			y, _ = strconv.Atoi(bSplit[i])
		}

		if x < y {
			return 1 // b is newer
		}

		if x > y {
			return -1 // a is newer
		}
	}

	return 0 // equal
}

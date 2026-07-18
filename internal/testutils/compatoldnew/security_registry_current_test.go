package compatoldnew

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// RunSecurityRegistryNoPodmanSocket starts the actual standalone registry role
// in Docker without a runtime socket mount, verifies the OCI endpoint, then
// inspects mounts, environment, and process descriptors on Linux.
func RunSecurityRegistryNoPodmanSocket(ctx context.Context, repoRoot, artifactDir string) (Report, error) {
	if runtime.GOOS != "linux" || repoRoot == "" || artifactDir == "" {
		return Report{}, fmt.Errorf("registry socket isolation requires Linux, repository, and artifact directory")
	}
	if err := DockerCompatibilityPreflight(ctx); err != nil {
		return Report{}, err
	}
	port, err := securityFreeLoopbackPort()
	if err != nil {
		return Report{}, err
	}
	root, err := os.MkdirTemp("", "gordon-compat-security-registry-*")
	if err != nil {
		return Report{}, err
	}
	defer os.RemoveAll(root)
	binary := filepath.Join(root, "gordon")
	if err := securityBuildCandidate(ctx, repoRoot, binary); err != nil {
		return Report{}, err
	}
	config := filepath.Join(root, "registry.toml")
	if err := os.WriteFile(config, []byte(securityRegistryConfig(port)), 0o600); err != nil {
		return Report{}, err
	}
	name := "gordon-compat-security-registry-" + sanitizePart(RunID("registry-isolation"))
	image := "gordon-compat-security-registry:" + sanitizePart(RunID("registry-image"))
	defer func() {
		_ = securityCommand(context.Background(), repoRoot, "docker", "rm", "--force", name)
		_ = securityCommand(context.Background(), repoRoot, "docker", "image", "rm", "--force", image)
	}()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\nCOPY gordon /gordon\nENTRYPOINT [\"/gordon\"]\n"), 0o600); err != nil {
		return Report{}, err
	}
	if err := securityCommand(ctx, root, "docker", "build", "--tag", image, "."); err != nil {
		return Report{}, err
	}
	if err := securityCommand(ctx, repoRoot, "docker", "run", "--detach", "--rm", "--network", "host", "--name", name, "--mount", "type=bind,source="+config+",target=/registry.toml,readonly", image, "serve", "--role", "registry", "--config", "/registry.toml"); err != nil {
		return Report{}, err
	}
	healthy, err := securityRegistryWorks(ctx, port)
	if err != nil {
		return Report{}, err
	}
	noMount, noEnv, noFD, err := securityContainerIsolation(ctx, repoRoot, name)
	if err != nil {
		return Report{}, err
	}
	return writeCurrentSecurityReport(artifactDir, map[string]bool{"registryOCIWorks": healthy, "noRuntimeSocketMount": noMount, "noRuntimeSocketEnv": noEnv, "noRuntimeSocketFD": noFD})
}

func securityRegistryConfig(port int) string {
	return fmt.Sprintf(`[storage]
data_dir = "/registry-data"
[listen]
address = "127.0.0.1:%d"
[listen.tls]
mode = "disabled"
[limits]
max_blob_chunk_size = "1KB"
max_blob_size = "1KB"
[auth]
enabled = false
[control]
event_endpoint = "127.0.0.1:1"
event_token = "gordon_component.registry-test-token"
insecure_tls = true
outbox_max_entries = 10
outbox_max_bytes = "1MB"
`, port)
}

func securityRegistryWorks(ctx context.Context, port int) (bool, error) {
	client := &http.Client{Timeout: time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/v2/", nil)
		if err != nil {
			return false, err
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && resp.Header.Get("Docker-Distribution-API-Version") == "registry/2.0" {
				return true, nil
			}
		}
		if ctx.Err() != nil {
			return false, fmt.Errorf("standalone registry did not serve OCI /v2 before timeout")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

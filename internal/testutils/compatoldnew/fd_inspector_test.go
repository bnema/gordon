package compatoldnew

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeSecurityFDInspection(t *testing.T) {
	inspection, err := decodeSecurityFDInspection([]byte(`{"authorityDetected":false,"inspectedFDCount":4,"socketFDCount":1,"authorityFDCount":0}`))
	require.NoError(t, err)
	require.False(t, inspection.AuthorityDetected)
	require.Equal(t, 4, inspection.InspectedFDCount)
	require.Equal(t, 1, inspection.SocketFDCount)
}

func TestDecodeSecurityFDInspectionRejectsInconsistentAuthorityResult(t *testing.T) {
	_, err := decodeSecurityFDInspection([]byte(`{"authorityDetected":false,"inspectedFDCount":4,"socketFDCount":1,"authorityFDCount":1}`))
	require.Error(t, err)
}

type securityFDInspection struct {
	AuthorityDetected bool `json:"authorityDetected"`
	InspectedFDCount  int  `json:"inspectedFDCount"`
	SocketFDCount     int  `json:"socketFDCount"`
	AuthorityFDCount  int  `json:"authorityFDCount"`
}

func decodeSecurityFDInspection(output []byte) (securityFDInspection, error) {
	var inspection securityFDInspection
	if err := json.Unmarshal(output, &inspection); err != nil {
		return securityFDInspection{}, fmt.Errorf("decode fd inspector output: %w", err)
	}
	if inspection.InspectedFDCount < 1 || inspection.SocketFDCount < 0 || inspection.AuthorityFDCount < 0 || inspection.SocketFDCount < inspection.AuthorityFDCount {
		return securityFDInspection{}, fmt.Errorf("fd inspector returned invalid descriptor counts")
	}
	if inspection.AuthorityDetected != (inspection.AuthorityFDCount > 0) {
		return securityFDInspection{}, fmt.Errorf("fd inspector returned inconsistent authority result")
	}
	return inspection, nil
}

// securityBuildFDInspector builds a static, test-only binary. The binary is
// copied into scratch images so Docker rootless mode never needs host /proc.
func securityBuildFDInspector(ctx context.Context, root string) (string, error) {
	source := filepath.Join(root, "fd-inspector.go")
	if err := os.WriteFile(source, []byte(securityFDInspectorSource), 0o600); err != nil {
		return "", fmt.Errorf("write fd inspector source: %w", err)
	}
	output := filepath.Join(root, "fd-inspector")
	cmd, err := newIsolatedCommand(ctx, "go", []string{"build", "-trimpath", "-ldflags=-s -w", "-o", output, source}, []string{"CGO_ENABLED=0"}, nil, false)
	if err != nil {
		return "", fmt.Errorf("prepare fd inspector build: %w", err)
	}
	cmd.Dir = root
	if _, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build fd inspector")
	}
	return output, nil
}

func securityInspectContainerFDs(ctx context.Context, repoRoot, name string) (securityFDInspection, error) {
	output, err := securityCommandOutput(ctx, repoRoot, "docker", "exec", name, "/fd-inspector")
	if err != nil {
		return securityFDInspection{}, fmt.Errorf("inspect container descriptors in namespace: %w", err)
	}
	return decodeSecurityFDInspection([]byte(output))
}

// securityFDInspectorNegativeControl proves the inspector sees named Unix
// sockets in the target namespace without revealing their paths in its output.
func securityFDInspectorNegativeControl(ctx context.Context, repoRoot, image string) error {
	name := "gordon-compat-fd-inspector-" + sanitizePart(RunID("negative"))
	defer func() { _ = securityCommand(context.Background(), repoRoot, "docker", "rm", "--force", name) }()
	if err := securityCommand(ctx, repoRoot, "docker", "run", "--detach", "--rm", "--network", "none", "--name", name, "--entrypoint", "/fd-inspector", image, "--hold-unix-socket", "/isolated-security-fd.sock"); err != nil {
		return fmt.Errorf("start fd inspector negative fixture: %w", err)
	}
	inspection, err := securityInspectContainerFDs(ctx, repoRoot, name)
	if err != nil {
		return fmt.Errorf("inspect fd inspector negative fixture: %w", err)
	}
	if inspection.AuthorityDetected || inspection.SocketFDCount < 1 {
		return fmt.Errorf("fd inspector negative fixture did not report its isolated named socket safely")
	}
	return nil
}

func securityImageDockerfile() string {
	return "FROM scratch\nCOPY gordon /gordon\nCOPY fd-inspector /fd-inspector\nENTRYPOINT [\"/gordon\"]\n"
}

const securityFDInspectorSource = `package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type inspection struct {
	AuthorityDetected bool
	InspectedFDCount int
	SocketFDCount int
	AuthorityFDCount int
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "--hold-unix-socket" {
		listener, err := net.Listen("unix", os.Args[2])
		if err != nil { panic(err) }
		defer listener.Close()
		for { time.Sleep(time.Hour) }
	}
	result, err := inspect("/proc/1")
	if err != nil { panic(err) }
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"authorityDetected": result.AuthorityDetected,
		"inspectedFDCount": result.InspectedFDCount,
		"socketFDCount": result.SocketFDCount,
		"authorityFDCount": result.AuthorityFDCount,
	}); err != nil { panic(err) }
}

func inspect(proc string) (inspection, error) {
	entries, err := os.ReadDir(filepath.Join(proc, "fd"))
	if err != nil { return inspection{}, err }
	paths, err := unixSocketPaths(filepath.Join(proc, "net", "unix"))
	if err != nil { return inspection{}, err }
	result := inspection{InspectedFDCount: len(entries)}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(proc, "fd", entry.Name()))
		if err != nil { continue }
		if inode, ok := socketInode(target); ok {
			result.SocketFDCount++
			target = paths[inode]
		}
		if isAuthority(target) {
			result.AuthorityFDCount++
		}
	}
	result.AuthorityDetected = result.AuthorityFDCount > 0
	return result, nil
}

func unixSocketPaths(path string) (map[string]string, error) {
	body, err := os.ReadFile(path)
	if err != nil { return nil, err }
	paths := make(map[string]string)
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 7 { paths[fields[6]] = fields[7] }
	}
	return paths, nil
}

func socketInode(target string) (string, bool) {
	if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") { return "", false }
	inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
	if _, err := strconv.ParseUint(inode, 10, 64); err != nil { return "", false }
	return inode, true
}

func isAuthority(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "docker") || strings.Contains(value, "podman") || strings.Contains(value, "containerd") || strings.Contains(value, "cri-dockerd") || strings.Contains(value, "crio") || strings.Contains(value, "/cri.sock") || strings.Contains(value, "/cri/")
}
`

package compatoldnew

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	OldBinaryName = "gordon-old"
	NewBinaryName = "gordon-new"
)

type BuildRequest struct {
	RepoRoot  string
	Ref       string
	OutputDir string
	Name      string
}

type BuildResult struct {
	BinaryPath string
	Ref        string
	Commit     string
}

type Builder interface {
	Build(context.Context, BuildRequest) (BuildResult, error)
}

type CommandRunner interface {
	Run(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	// #nosec G204 -- compatibility tests intentionally execute caller-provided git/go commands.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

type GoBuilder struct {
	Runner CommandRunner
}

func (b GoBuilder) Build(ctx context.Context, req BuildRequest) (BuildResult, error) {
	runner := b.Runner
	if runner == nil {
		runner = ExecCommandRunner{}
	}
	if req.RepoRoot == "" || req.OutputDir == "" || req.Name == "" {
		return BuildResult{}, fmt.Errorf("build %q: repo root, output dir, and name are required", req.Ref)
	}
	outputDir, err := filepath.Abs(req.OutputDir)
	if err != nil {
		return BuildResult{}, fmt.Errorf("build %q: resolve output dir: %w", req.Ref, err)
	}
	if err := os.MkdirAll(outputDir, 0o750); err != nil {
		return BuildResult{}, fmt.Errorf("build %q: create output dir: %w", req.Ref, err)
	}
	ref := req.Ref
	if ref == "" {
		ref = "HEAD"
	}
	commitOut, err := runner.Run(ctx, req.RepoRoot, "git", "rev-parse", ref)
	if err != nil {
		return BuildResult{}, fmt.Errorf("build ref %q resolve commit: %w", ref, err)
	}
	commit := strings.TrimSpace(string(commitOut))
	binaryPath := filepath.Join(outputDir, req.Name)
	if req.Name == NewBinaryName {
		if _, err := runner.Run(ctx, req.RepoRoot, "go", "build", "-o", binaryPath, "./main.go"); err != nil {
			return BuildResult{}, fmt.Errorf("build ref %q candidate command go build: %w", ref, err)
		}
		return BuildResult{BinaryPath: binaryPath, Ref: ref, Commit: commit}, nil
	}
	tmp, err := os.MkdirTemp("", "gordon-compat-build-*")
	if err != nil {
		return BuildResult{}, fmt.Errorf("build ref %q create temp checkout: %w", ref, err)
	}
	defer os.RemoveAll(tmp)
	if _, err := runner.Run(ctx, req.RepoRoot, "git", "worktree", "add", "--detach", tmp, ref); err != nil {
		return BuildResult{}, fmt.Errorf("build ref %q worktree add: %w", ref, err)
	}
	defer func() {
		_, _ = runner.Run(context.Background(), req.RepoRoot, "git", "worktree", "remove", "--force", tmp)
	}()
	if _, err := runner.Run(ctx, tmp, "go", "build", "-o", binaryPath, "./main.go"); err != nil {
		return BuildResult{}, fmt.Errorf("build ref %q baseline command go build: %w", ref, err)
	}
	return BuildResult{BinaryPath: binaryPath, Ref: ref, Commit: commit}, nil
}

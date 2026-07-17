package compatoldnew

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	OldBinaryName                  = "gordon-old"
	NewBinaryName                  = "gordon-new"
	detachedWorktreeCleanupTimeout = 10 * time.Second
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

// OldNewBinaries identifies the independently built baseline and candidate.
type OldNewBinaries struct {
	Old BuildResult
	New BuildResult
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

// BuildOldAndNew builds the configured baseline in a detached worktree and the
// candidate from the caller's current working tree, in that order.
func BuildOldAndNew(ctx context.Context, builder Builder, repoRoot, outputDir string) (OldNewBinaries, error) {
	if builder == nil {
		builder = GoBuilder{}
	}
	oldBuild, err := builder.Build(ctx, BuildRequest{RepoRoot: repoRoot, Ref: BaselineRefFromEnv(), OutputDir: outputDir, Name: OldBinaryName})
	if err != nil {
		return OldNewBinaries{}, fmt.Errorf("build compatibility baseline: %w", err)
	}
	newBuild, err := builder.Build(ctx, BuildRequest{RepoRoot: repoRoot, Ref: "HEAD", OutputDir: outputDir, Name: NewBinaryName})
	if err != nil {
		return OldNewBinaries{}, fmt.Errorf("build compatibility candidate: %w", err)
	}
	return OldNewBinaries{Old: oldBuild, New: newBuild}, nil
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
		return buildCandidate(ctx, runner, req, ref, commit, binaryPath)
	}
	return buildDetachedBaseline(ctx, runner, req, ref, commit, binaryPath)
}

func buildCandidate(ctx context.Context, runner CommandRunner, req BuildRequest, ref, commit, binaryPath string) (BuildResult, error) {
	if _, err := runner.Run(ctx, req.RepoRoot, "go", "build", "-o", binaryPath, "./main.go"); err != nil {
		return BuildResult{}, fmt.Errorf("build ref %q candidate command go build: %w", ref, err)
	}
	return BuildResult{BinaryPath: binaryPath, Ref: ref, Commit: commit}, nil
}

func buildDetachedBaseline(ctx context.Context, runner CommandRunner, req BuildRequest, ref, commit, binaryPath string) (BuildResult, error) {
	tmp, err := os.MkdirTemp("", "gordon-compat-build-*")
	if err != nil {
		return BuildResult{}, fmt.Errorf("build ref %q create temp checkout: %w", ref, err)
	}
	defer os.RemoveAll(tmp)
	if _, err := runner.Run(ctx, req.RepoRoot, "git", "worktree", "add", "--detach", tmp, ref); err != nil {
		return BuildResult{}, fmt.Errorf("build ref %q worktree add: %w", ref, err)
	}
	_, buildErr := runner.Run(ctx, tmp, "go", "build", "-o", binaryPath, "./main.go")
	cleanupErr := removeDetachedWorktree(runner, req.RepoRoot, tmp)
	if buildErr != nil {
		buildErr = fmt.Errorf("build ref %q baseline command go build: %w", ref, buildErr)
		if cleanupErr != nil {
			return BuildResult{}, errors.Join(buildErr, cleanupErr)
		}
		return BuildResult{}, buildErr
	}
	if cleanupErr != nil {
		return BuildResult{}, cleanupErr
	}
	return BuildResult{BinaryPath: binaryPath, Ref: ref, Commit: commit}, nil
}

func removeDetachedWorktree(runner CommandRunner, repoRoot, path string) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), detachedWorktreeCleanupTimeout)
	defer cancel()
	if _, err := runner.Run(cleanupCtx, repoRoot, "git", "worktree", "remove", "--force", path); err != nil {
		return fmt.Errorf("remove detached baseline worktree: %w", err)
	}
	return nil
}

package compatoldnew

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordedCommand struct {
	dir, name string
	args      []string
}
type fakeRunner struct{ commands []recordedCommand }

func (f *fakeRunner) Run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	f.commands = append(f.commands, recordedCommand{dir: dir, name: name, args: append([]string{}, args...)})
	if name == "git" && len(args) == 2 && args[0] == "rev-parse" {
		if args[1] == "HEAD" {
			return []byte("def456\n"), nil
		}
		return []byte("abc123\n"), nil
	}
	return []byte{}, nil
}

func TestBuildOldAndNewUsesBaselineAndCurrentWorkingTreeWithoutBranchMutation(t *testing.T) {
	t.Setenv(EnvCompatBaselineRef, "refs/tags/v0.9.0")
	fr := &fakeRunner{}
	binaries, err := BuildOldAndNew(context.Background(), GoBuilder{Runner: fr}, "/repo", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if binaries.Old.Ref != "refs/tags/v0.9.0" || binaries.New.Ref != "HEAD" {
		t.Fatalf("unexpected build refs: %+v", binaries)
	}
	for _, command := range fr.commands {
		if command.name == "git" && (len(command.args) > 0 && (command.args[0] == "checkout" || command.args[0] == "switch")) {
			t.Fatalf("branch mutation: %#v", command)
		}
	}
}

type sameCommitRunner struct{ fakeRunner }

func (f *sameCommitRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	if name == "git" && len(args) == 2 && args[0] == "rev-parse" {
		f.commands = append(f.commands, recordedCommand{dir: dir, name: name, args: append([]string{}, args...)})
		return []byte("same-commit\n"), nil
	}
	return f.fakeRunner.Run(ctx, dir, name, args...)
}

func TestBuildOldAndNewRejectsBaselineThatResolvesToCandidate(t *testing.T) {
	fr := &sameCommitRunner{}
	_, err := BuildOldAndNew(context.Background(), GoBuilder{Runner: fr}, "/repo", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "same commit") {
		t.Fatalf("expected self-comparison refusal, got %v", err)
	}
}

func TestGoBuilderBuildsCandidateFromCurrentWorkingTree(t *testing.T) {
	t.Chdir(t.TempDir())
	fr := &fakeRunner{}
	res, err := (GoBuilder{Runner: fr}).Build(context.Background(), BuildRequest{RepoRoot: "/repo", Ref: "HEAD", OutputDir: "relative-out", Name: NewBinaryName})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.BinaryPath, "/"+NewBinaryName) || res.Commit != "def456" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !filepath.IsAbs(res.BinaryPath) {
		t.Fatalf("binary path should be absolute: %s", res.BinaryPath)
	}
	if len(fr.commands) != 2 {
		t.Fatalf("commands=%v", fr.commands)
	}
	build := fr.commands[1]
	if build.dir != "/repo" || build.name != "go" || strings.Join(build.args, " ") != "build -o "+res.BinaryPath+" ./main.go" {
		t.Fatalf("unexpected build command: %+v", build)
	}
}

type cleanupFailureRunner struct {
	fakeRunner
	cleanupErr      error
	cleanupDeadline bool
	worktreePath    string
}

func (f *cleanupFailureRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	if name == "git" && len(args) >= 4 && strings.Join(args[:3], " ") == "worktree add --detach" {
		f.worktreePath = args[3]
	}
	if name == "git" && len(args) >= 3 && strings.Join(args[:3], " ") == "worktree remove --force" {
		_, f.cleanupDeadline = ctx.Deadline()
		return nil, f.cleanupErr
	}
	return f.fakeRunner.Run(ctx, dir, name, args...)
}

func TestGoBuilderSurfacesBoundedWorktreeCleanupFailures(t *testing.T) {
	for _, cleanupErr := range []error{context.DeadlineExceeded, errors.New("worktree still locked")} {
		t.Run(cleanupErr.Error(), func(t *testing.T) {
			fr := &cleanupFailureRunner{cleanupErr: cleanupErr}
			_, err := (GoBuilder{Runner: fr}).Build(context.Background(), BuildRequest{
				RepoRoot: "/repo", Ref: "v1.0.0", OutputDir: t.TempDir(), Name: OldBinaryName,
			})
			if !errors.Is(err, cleanupErr) {
				t.Fatalf("cleanup error was not surfaced: %v", err)
			}
			if !fr.cleanupDeadline {
				t.Fatal("worktree cleanup did not receive a bounded context")
			}
			if _, statErr := os.Stat(fr.worktreePath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("temporary worktree fallback was not removed: %v", statErr)
			}
		})
	}
}

func TestGoBuilderBaselineUsesDetachedWorktreeAndDoesNotCheckoutCurrentBranch(t *testing.T) {
	fr := &fakeRunner{}
	res, err := (GoBuilder{Runner: fr}).Build(context.Background(), BuildRequest{RepoRoot: "/repo", Ref: "v1.0.0", OutputDir: t.TempDir(), Name: OldBinaryName})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.BinaryPath, "/"+OldBinaryName) {
		t.Fatalf("unexpected binary: %s", res.BinaryPath)
	}
	seenWorktreeAdd, seenRemove := false, false
	for _, c := range fr.commands {
		joined := strings.Join(append([]string{c.name}, c.args...), " ")
		if strings.Contains(joined, "git checkout") || strings.Contains(joined, "git switch") {
			t.Fatalf("mutating branch command used: %s", joined)
		}
		if c.name == "git" && len(c.args) >= 5 && strings.Join(c.args[:3], " ") == "worktree add --detach" && c.args[4] == "v1.0.0" {
			seenWorktreeAdd = true
		}
		if c.name == "git" && len(c.args) >= 3 && strings.Join(c.args[:3], " ") == "worktree remove --force" {
			seenRemove = true
		}
	}
	if !seenWorktreeAdd || !seenRemove {
		t.Fatalf("expected worktree add/remove, got %#v", fr.commands)
	}
}

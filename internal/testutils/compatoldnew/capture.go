package compatoldnew

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ComparisonLevel describes how strictly an artifact must compare.
type ComparisonLevel string

const (
	LevelExact                 ComparisonLevel = "exact"
	LevelSemantic              ComparisonLevel = "semantic"
	LevelPresence              ComparisonLevel = "presence"
	LevelSecurityNegative      ComparisonLevel = "security_negative"
	LevelAllowlistedDifference ComparisonLevel = "allowlisted_difference"
)

// Artifact is the common compatibility artifact shape.
type Artifact interface {
	ArtifactType() string
	Source() string
	RawValue() any
	NormalizedValue() any
	Level() ComparisonLevel
}

type baseArtifact struct {
	Raw        any             `json:"raw"`
	Normalized any             `json:"normalized"`
	SourceRef  string          `json:"source"`
	Compare    ComparisonLevel `json:"comparisonLevel"`
}

func (a baseArtifact) Source() string         { return a.SourceRef }
func (a baseArtifact) RawValue() any          { return a.Raw }
func (a baseArtifact) NormalizedValue() any   { return a.Normalized }
func (a baseArtifact) Level() ComparisonLevel { return a.Compare }

// CLIArtifact captures command output and exit status.
type CLIArtifact struct{ baseArtifact }

func (CLIArtifact) ArtifactType() string { return "cli" }

// HTTPArtifact captures an HTTP request/response exchange.
type HTTPArtifact struct{ baseArtifact }

func (HTTPArtifact) ArtifactType() string { return "http" }

// RegistryArtifact captures registry API/content observations.
type RegistryArtifact struct{ baseArtifact }

func (RegistryArtifact) ArtifactType() string { return "registry" }

// ProxyArtifact captures reverse-proxy observations.
type ProxyArtifact struct{ baseArtifact }

func (ProxyArtifact) ArtifactType() string { return "proxy" }

// RuntimeArtifact captures runtime observations without exposing a runtime API.
type RuntimeArtifact struct{ baseArtifact }

func (RuntimeArtifact) ArtifactType() string { return "runtime" }

// ConfigArtifact captures configuration observations.
type ConfigArtifact struct{ baseArtifact }

func (ConfigArtifact) ArtifactType() string { return "config" }

// LogArtifact captures log observations.
type LogArtifact struct{ baseArtifact }

func (LogArtifact) ArtifactType() string { return "log" }

// MigrationArtifact captures migration observations.
type MigrationArtifact struct{ baseArtifact }

func (MigrationArtifact) ArtifactType() string { return "migration" }

func newBaseArtifact(source string, raw any, level ComparisonLevel) baseArtifact {
	return baseArtifact{Raw: raw, Normalized: Normalize(raw), SourceRef: source, Compare: level}
}

func NewCLIArtifact(source string, raw any, level ComparisonLevel) CLIArtifact {
	return CLIArtifact{newBaseArtifact(source, raw, level)}
}

// CommandCaptureRequest describes one CLI invocation. Environment values are
// intentionally retained only as redacted metadata in the resulting artifact.
type CommandCaptureRequest struct {
	BinaryPath string
	Args       []string
	Dir        string
	Env        []string
	Source     string
	Level      ComparisonLevel
}

// CaptureCommand records independent stdout, stderr, and process exit status.
// A non-zero process exit is an observation to compare, not a capture failure.
func CaptureCommand(ctx context.Context, request CommandCaptureRequest) (CLIArtifact, error) {
	if request.BinaryPath == "" {
		return CLIArtifact{}, fmt.Errorf("capture command: binary path is required")
	}
	source := request.Source
	if source == "" {
		source = strings.Join(append([]string{request.BinaryPath}, request.Args...), " ")
	}
	source = redactMetadata(source)
	level := request.Level
	if level == "" {
		level = LevelExact
	}
	// #nosec G204 -- compatibility tests intentionally execute a selected binary.
	cmd := exec.CommandContext(ctx, request.BinaryPath, request.Args...)
	cmd.Dir = request.Dir
	cmd.Env = commandEnvironment(request.Env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return CLIArtifact{}, fmt.Errorf("capture command %q: %w", source, err)
		}
		exitCode = exitErr.ExitCode()
	}
	raw := map[string]any{
		"command":     redactMetadata(request.BinaryPath),
		"args":        redactArguments(request.Args),
		"environment": redactedEnvironment(request.Env),
		"exitCode":    exitCode,
		"stdout":      stdout.String(),
		"stderr":      stderr.String(),
	}
	return NewCLIArtifact(source, raw, level), nil
}

func redactedEnvironment(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if key != "" && ok {
			values[key] = "<redacted>"
		}
	}
	return values
}

func redactArguments(args []string) []string {
	redacted := make([]string, len(args))
	copy(redacted, args)
	for i, arg := range redacted {
		redacted[i] = redactMetadata(arg)
		if (arg == "--token" || arg == "--authorization") && i+1 < len(redacted) {
			redacted[i+1] = "<redacted>"
		}
	}
	return redacted
}

// ExecuteSide captures a command and associates it with exactly one old/new side.
func ExecuteSide(ctx context.Context, side string, request CommandCaptureRequest) (SideResult, error) {
	if side != SideOld && side != SideNew {
		return SideResult{}, fmt.Errorf("execute side: unknown side %q", side)
	}
	artifact, err := CaptureCommand(ctx, request)
	if err != nil {
		return SideResult{}, err
	}
	return SideResult{Side: side, Artifact: artifact}, nil
}

func redactMetadata(value string) string {
	fields := strings.Fields(value)
	for i, field := range fields {
		lower := strings.ToLower(field)
		if strings.Contains(lower, "token=") || strings.Contains(lower, "authorization=") {
			key, _, ok := strings.Cut(field, "=")
			if ok {
				fields[i] = key + "=<redacted>"
			}
		}
		if (field == "--token" || field == "--authorization") && i+1 < len(fields) {
			fields[i+1] = "<redacted>"
		}
	}
	return strings.Join(fields, " ")
}

func NewHTTPArtifact(source string, raw any, level ComparisonLevel) HTTPArtifact {
	return HTTPArtifact{newBaseArtifact(source, raw, level)}
}

func NewRegistryArtifact(source string, raw any, level ComparisonLevel) RegistryArtifact {
	return RegistryArtifact{newBaseArtifact(source, raw, level)}
}

func NewProxyArtifact(source string, raw any, level ComparisonLevel) ProxyArtifact {
	return ProxyArtifact{newBaseArtifact(source, raw, level)}
}

func NewRuntimeArtifact(source string, raw any, level ComparisonLevel) RuntimeArtifact {
	return RuntimeArtifact{newBaseArtifact(source, raw, level)}
}

func NewConfigArtifact(source string, raw any, level ComparisonLevel) ConfigArtifact {
	return ConfigArtifact{newBaseArtifact(source, raw, level)}
}

func NewLogArtifact(source string, raw any, level ComparisonLevel) LogArtifact {
	return LogArtifact{newBaseArtifact(source, raw, level)}
}

func NewMigrationArtifact(source string, raw any, level ComparisonLevel) MigrationArtifact {
	return MigrationArtifact{newBaseArtifact(source, raw, level)}
}

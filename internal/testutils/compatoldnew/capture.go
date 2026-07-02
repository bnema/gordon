package compatoldnew

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

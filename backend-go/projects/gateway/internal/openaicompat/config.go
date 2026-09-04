package openaicompat

// Config carries the runtime-config surface the five Node modules read from
// backend/src/config/runtime.ts. Defaults mirror the Node fallbacks; roots
// are configurable so deployments (and tests) can isolate the physical
// layout while default behavior stays identical.
type Config struct {
	// FilesRoot mirrors runtimeConfig.openAICompatibleFilesRoot
	// (JUHE_AI_OPENAI_COMPATIBLE_FILES_ROOT, default <backendRoot>/data/openai-compatible-files).
	FilesRoot string

	// Port mirrors runtimeConfig.port for the default image-generation
	// provider endpoint http://127.0.0.1:<port>/v1/images/generations.
	Port int

	// MaxFileBytes mirrors openAICompatibleFileMaxBytes (512 MiB upload cap).
	// A config override exists only so tests can exercise the 413 boundary
	// without a 512 MiB payload; zero means the Node default.
	MaxFileBytes int64

	// CodeInterpreter mirrors runtimeConfig.codeInterpreter.
	CodeInterpreter CodeInterpreterConfig

	// ComputerAdapter mirrors runtimeConfig.computerAdapter.
	ComputerAdapter ComputerAdapterConfig

	// HostedToolCodeInterpreterMode / HostedToolComputerMode mirror
	// runtimeConfig.hostedToolRuntimes (guidance default).
	HostedToolCodeInterpreterMode string
	HostedToolComputerMode        string
}

// CodeInterpreterConfig mirrors RuntimeConfig['codeInterpreter'].
type CodeInterpreterConfig struct {
	PythonCommand        string
	TimeoutMs            int64
	MaxCodeBytes         int64
	MaxOutputBytes       int64
	MaxArtifactCount     int
	MaxArtifactBytes     int64
	TempRoot             string
	CleanupTempDirectory bool
}

// ComputerAdapterConfig mirrors RuntimeConfig['computerAdapter'].
type ComputerAdapterConfig struct {
	Enabled      bool
	Endpoint     string
	TimeoutMs    int64
	MaxBodyBytes int64
}

const (
	// DefaultMaxFileBytes mirrors openAICompatibleFileMaxBytes.
	DefaultMaxFileBytes = 512 * 1024 * 1024
	// BridgeMaxFileBytes mirrors openAICompatibleBridgeFileMaxBytes (32 MiB).
	BridgeMaxFileBytes = 32 * 1024 * 1024
	// ImageGenerationProviderModel mirrors imageGenerationProviderModel.
	ImageGenerationProviderModel = "gpt-image-2"
	// ImageGenerationProviderTimeoutMs mirrors the 600s default.
	ImageGenerationProviderTimeoutMs = 600_000
	// ImageGenerationProviderMaxBodyBytes mirrors the 64 MiB default.
	ImageGenerationProviderMaxBodyBytes = 64 * 1024 * 1024
)

func (c CodeInterpreterConfig) withDefaults() CodeInterpreterConfig {
	if c.PythonCommand == "" {
		c.PythonCommand = "python"
	}
	if c.TimeoutMs <= 0 {
		c.TimeoutMs = 5000
	}
	if c.MaxCodeBytes <= 0 {
		c.MaxCodeBytes = 64 * 1024
	}
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = 64 * 1024
	}
	if c.MaxArtifactCount <= 0 {
		c.MaxArtifactCount = 8
	}
	if c.MaxArtifactBytes <= 0 {
		c.MaxArtifactBytes = 256 * 1024
	}
	if c.TempRoot == "" {
		c.TempRoot = "data/code-interpreter-tmp"
	}
	return c
}

func (c ComputerAdapterConfig) withDefaults() ComputerAdapterConfig {
	if c.TimeoutMs <= 0 {
		c.TimeoutMs = 30000
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = 512 * 1024
	}
	return c
}

func (c Config) withDefaults() Config {
	if c.MaxFileBytes <= 0 {
		c.MaxFileBytes = DefaultMaxFileBytes
	}
	if c.FilesRoot == "" {
		c.FilesRoot = "data/openai-compatible-files"
	}
	c.CodeInterpreter = c.CodeInterpreter.withDefaults()
	c.ComputerAdapter = c.ComputerAdapter.withDefaults()
	return c
}

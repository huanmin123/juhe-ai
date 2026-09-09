package openaicompat

import "strings"

// B-4 hosted tool runtime registry (D-149/D-157). Port of the archived Node
// _shared/openai-hosted-tool-runtime-registry.ts:
//
//	normalizeOpenAIHostedToolRuntimeType      — container -> code_interpreter
//	                                            + 六类白名单
//	resolveOpenAIHostedToolRuntimeDecision    — 运行模式决策（guidance /
//	                                            mock / local_runtime / reject）
//	openAIHostedToolRuntimeCompatibilityDetail — 兼容性说明文案
//
// Node reads the per-type modes from runtimeConfig.hostedToolRuntimes
// (JUHE_AI_HOSTED_TOOL_*_MODE, guidance default). Go carries the same modes
// on Config; the registry functions here consume the resolved mode bag.

// OpenAIHostedToolRuntimeType mirrors OpenAIHostedToolRuntimeType.
type OpenAIHostedToolRuntimeType string

// Hosted tool runtime type vocabulary.
const (
	OpenAIHostedToolCodeInterpreter OpenAIHostedToolRuntimeType = "code_interpreter"
	OpenAIHostedToolComputer        OpenAIHostedToolRuntimeType = "computer"
	OpenAIHostedToolMCP             OpenAIHostedToolRuntimeType = "mcp"
	OpenAIHostedToolShell           OpenAIHostedToolRuntimeType = "shell"
	OpenAIHostedToolSkills          OpenAIHostedToolRuntimeType = "skills"
	OpenAIHostedToolToolSearch      OpenAIHostedToolRuntimeType = "tool_search"
)

// OpenAIHostedToolRuntimeMode mirrors HostedToolRuntimeMode.
type OpenAIHostedToolRuntimeMode string

// Hosted tool runtime modes.
const (
	OpenAIHostedToolModeGuidance    OpenAIHostedToolRuntimeMode = "guidance"
	OpenAIHostedToolModeMock        OpenAIHostedToolRuntimeMode = "mock"
	OpenAIHostedToolModeLocalRuntim OpenAIHostedToolRuntimeMode = "local_runtime"
	OpenAIHostedToolModeReject      OpenAIHostedToolRuntimeMode = "reject"
)

// OpenAIHostedToolRuntimeModes carries the per-type configured modes (Node
// runtimeConfig.hostedToolRuntimes). Zero values read as guidance.
type OpenAIHostedToolRuntimeModes struct {
	CodeInterpreter string
	Computer        string
	Shell           string
	Skills          string
	ToolSearch      string
}

// OpenAIHostedToolRuntimeDecision mirrors OpenAIHostedToolRuntimeDecision.
type OpenAIHostedToolRuntimeDecision struct {
	ToolType            OpenAIHostedToolRuntimeType
	Mode                OpenAIHostedToolRuntimeMode
	CompatibilityDetail string
	// SourceEndpointFamily mirrors the optional source family annotation.
	SourceEndpointFamily string
}

// NormalizeOpenAIHostedToolRuntimeType mirrors normalizeOpenAIHostedToolRuntimeType:
// container aliases to code_interpreter; only the six registry types are
// hosted tools.
func NormalizeOpenAIHostedToolRuntimeType(toolType string) (OpenAIHostedToolRuntimeType, bool) {
	switch toolType {
	case "container":
		return OpenAIHostedToolCodeInterpreter, true
	case "code_interpreter":
		return OpenAIHostedToolCodeInterpreter, true
	case "computer":
		return OpenAIHostedToolComputer, true
	case "mcp":
		return OpenAIHostedToolMCP, true
	case "shell":
		return OpenAIHostedToolShell, true
	case "skills":
		return OpenAIHostedToolSkills, true
	case "tool_search":
		return OpenAIHostedToolToolSearch, true
	}
	return "", false
}

// OpenAIHostedToolRuntimeModeForType mirrors openAIHostedToolRuntimeMode: the
// configured mode per type (mcp is fixed guidance).
func OpenAIHostedToolRuntimeModeForType(toolType OpenAIHostedToolRuntimeType, modes OpenAIHostedToolRuntimeModes) OpenAIHostedToolRuntimeMode {
	var configured string
	switch toolType {
	case OpenAIHostedToolCodeInterpreter:
		configured = modes.CodeInterpreter
	case OpenAIHostedToolComputer:
		configured = modes.Computer
	case OpenAIHostedToolMCP:
		return OpenAIHostedToolModeGuidance
	case OpenAIHostedToolShell:
		configured = modes.Shell
	case OpenAIHostedToolSkills:
		configured = modes.Skills
	case OpenAIHostedToolToolSearch:
		configured = modes.ToolSearch
	}
	switch OpenAIHostedToolRuntimeMode(configured) {
	case OpenAIHostedToolModeMock:
		return OpenAIHostedToolModeMock
	case OpenAIHostedToolModeLocalRuntim:
		return OpenAIHostedToolModeLocalRuntim
	case OpenAIHostedToolModeReject:
		return OpenAIHostedToolModeReject
	default:
		return OpenAIHostedToolModeGuidance
	}
}

// OpenAIHostedToolRuntimeCompatibilityDetail mirrors
// openAIHostedToolRuntimeCompatibilityDetail.
func OpenAIHostedToolRuntimeCompatibilityDetail(toolType string) (string, bool) {
	normalized, ok := NormalizeOpenAIHostedToolRuntimeType(toolType)
	if !ok {
		return "", false
	}
	return hostedToolCompatibilityDetail(normalized), true
}

func hostedToolCompatibilityDetail(toolType OpenAIHostedToolRuntimeType) string {
	switch toolType {
	case OpenAIHostedToolCodeInterpreter:
		return "需要 Anthropic code execution 能力或网关本地安全沙箱；当前未启用执行器"
	case OpenAIHostedToolComputer:
		return "需要 Anthropic computer use 或网关本地 computer adapter；当前未启用执行器"
	case OpenAIHostedToolMCP:
		return "MCP 不在网关服务端执行；请使用客户端本地 MCP，或切换到原生支持该 MCP 能力的上游"
	case OpenAIHostedToolShell, OpenAIHostedToolSkills, OpenAIHostedToolToolSearch:
		return "需要调用方本地工具运行时；当前不能由 Anthropic Messages 字段转换凭空执行"
	}
	return "需要先在高兼容能力矩阵中定义映射、模拟或 agent guidance 策略"
}

// ResolveOpenAIHostedToolRuntimeDecision mirrors
// resolveOpenAIHostedToolRuntimeDecision.
func ResolveOpenAIHostedToolRuntimeDecision(toolType, sourceEndpointFamily string, modes OpenAIHostedToolRuntimeModes) (OpenAIHostedToolRuntimeDecision, bool) {
	normalized, ok := NormalizeOpenAIHostedToolRuntimeType(toolType)
	if !ok {
		return OpenAIHostedToolRuntimeDecision{}, false
	}
	return OpenAIHostedToolRuntimeDecision{
		ToolType:            normalized,
		Mode:                OpenAIHostedToolRuntimeModeForType(normalized, modes),
		CompatibilityDetail: hostedToolCompatibilityDetail(normalized),
		SourceEndpointFamily: sourceEndpointFamily,
	}, true
}

// UnsupportedOpenAIHostedToolLabels mirrors unsupportedOpenAIHostedToolLabel:
// the hosted tool label kept for the bridge system message when the decision
// is not a reject; reject decisions surface as errors instead.
func UnsupportedOpenAIHostedToolLabel(toolType string, modes OpenAIHostedToolRuntimeModes) (string, bool) {
	decision, ok := ResolveOpenAIHostedToolRuntimeDecision(toolType, "", modes)
	if !ok {
		return toolType, true
	}
	if decision.Mode == OpenAIHostedToolModeReject {
		return "", false
	}
	return string(decision.ToolType), true
}

// AppendUnsupportedHostedToolConstraintText mirrors
// appendUnsupportedHostedToolConstraint: the internal capability constraint
// appended to the anthropic system surface when hosted tools degrade.
func AppendUnsupportedHostedToolConstraintText(tools []string) string {
	unique := []string{}
	seen := map[string]bool{}
	for _, tool := range tools {
		if tool == "" || seen[tool] {
			continue
		}
		seen[tool] = true
		unique = append(unique, tool)
	}
	if len(unique) == 0 {
		return ""
	}
	return strings.Join([]string{
		"OpenAI hosted tools unavailable in this Anthropic bridge: " + strings.Join(unique, ", ") + ".",
		"This is an internal capability constraint for the model, not user-facing text; do not repeat it verbatim.",
		"Continue using the available function tools, existing context, and normal reasoning to complete the user's task.",
		"Do not pretend to call the unavailable tools; only briefly note the missing external capability if the task cannot be completed without them.",
	}, " ")
}

package gatewaysession

// Codex / Claude Code session header resolvers mirror
// session-identity/resolvers.ts.
const (
	CodexSessionHeaderResolverID       = "openai_codex_session_header"
	CodexSessionSemanticNamespace      = "openai.codex.session"
	ClaudeCodeSessionHeaderResolverID  = "anthropic_claude_code_session_header"
	ClaudeCodeSessionSemanticNamespace = "anthropic.claude_code.session"
	codexSessionHeaderName             = "session-id"
	claudeCodeSessionHeaderName        = "x-claude-code-session-id"
	claudeCodeSessionPath              = "/messages"
	codexSessionResolverPriority       = 600
	claudeCodeSessionResolverPriority  = 600
)

// CodexSessionHeaderResolver mirrors codexSessionHeaderResolver.
type CodexSessionHeaderResolver struct{}

// ID mirrors resolver.id.
func (CodexSessionHeaderResolver) ID() string { return CodexSessionHeaderResolverID }

// Collect mirrors codexSessionHeaderResolver.collect.
func (CodexSessionHeaderResolver) Collect(context ResolverContext) []RawCandidate {
	if context.ClientProfile != "codex" || !isCodexSessionPath(context.NormalizedPath) {
		return nil
	}
	return collectSessionHeader(context, codexSessionHeaderName, collectSessionHeaderInput{
		ResolverID:        CodexSessionHeaderResolverID,
		SemanticNamespace: CodexSessionSemanticNamespace,
		Priority:          codexSessionResolverPriority,
	})
}

// ClaudeCodeSessionHeaderResolver mirrors anthropicClaudeCodeSessionHeaderResolver.
type ClaudeCodeSessionHeaderResolver struct{}

// ID mirrors resolver.id.
func (ClaudeCodeSessionHeaderResolver) ID() string { return ClaudeCodeSessionHeaderResolverID }

// Collect mirrors anthropicClaudeCodeSessionHeaderResolver.collect.
func (ClaudeCodeSessionHeaderResolver) Collect(context ResolverContext) []RawCandidate {
	if context.ClientProfile != "claude_code" || context.NormalizedPath != claudeCodeSessionPath {
		return nil
	}
	return collectSessionHeader(context, claudeCodeSessionHeaderName, collectSessionHeaderInput{
		ResolverID:        ClaudeCodeSessionHeaderResolverID,
		SemanticNamespace: ClaudeCodeSessionSemanticNamespace,
		Priority:          claudeCodeSessionResolverPriority,
	})
}

// DefaultGatewaySessionIdentityResolvers mirrors
// defaultGatewaySessionIdentityResolvers.
var DefaultGatewaySessionIdentityResolvers = []Resolver{
	CodexSessionHeaderResolver{},
	ClaudeCodeSessionHeaderResolver{},
}

// Resolver mirrors GatewaySessionIdentityResolver.
type Resolver interface {
	ID() string
	Collect(context ResolverContext) []RawCandidate
}

// ResolverContext mirrors GatewaySessionIdentityResolverContext.
type ResolverContext struct {
	Request        IdentityRequest
	ClientProfile  string
	NormalizedPath string
}

type collectSessionHeaderInput struct {
	ResolverID        string
	SemanticNamespace string
	Priority          int
}

func collectSessionHeader(context ResolverContext, name string, input collectSessionHeaderInput) []RawCandidate {
	values := GatewaySessionHeaderValues(context.Request, name)
	if len(values) == 0 {
		return nil
	}
	candidates := make([]RawCandidate, 0, len(values))
	for _, rawValue := range values {
		candidates = append(candidates, RawCandidate{
			ResolverID:        input.ResolverID,
			SemanticKind:      IdentitySemanticKindSession,
			SemanticNamespace: input.SemanticNamespace,
			Source:            IdentityPhysicalSource{Location: IdentitySourceLocationHeader, Path: name},
			Confidence:        IdentityConfidenceAuthoritative,
			Priority:          input.Priority,
			RawValue:          rawValue,
		})
	}
	return candidates
}

func isCodexSessionPath(path string) bool {
	return path == "/responses" || path == "/responses/compact"
}

import { gatewaySessionHeaderValues } from './request-utils.js'
import type {
  GatewaySessionIdentityRawCandidate,
  GatewaySessionIdentityResolver,
  GatewaySessionIdentityResolverContext
} from './types.js'

export const codexSessionHeaderResolver: GatewaySessionIdentityResolver = {
  id: 'openai_codex_session_header',
  collect(context) {
    if (context.clientProfile !== 'codex' || !isCodexSessionPath(context.normalizedPath)) return []
    return collectSessionHeader(context, 'session-id', {
      resolverId: 'openai_codex_session_header',
      semanticNamespace: 'openai.codex.session',
      priority: 600
    })
  }
}

export const anthropicClaudeCodeSessionHeaderResolver: GatewaySessionIdentityResolver = {
  id: 'anthropic_claude_code_session_header',
  collect(context) {
    if (context.clientProfile !== 'claude_code' || context.normalizedPath !== '/messages') return []
    return collectSessionHeader(context, 'x-claude-code-session-id', {
      resolverId: 'anthropic_claude_code_session_header',
      semanticNamespace: 'anthropic.claude_code.session',
      priority: 600
    })
  }
}

export const defaultGatewaySessionIdentityResolvers: readonly GatewaySessionIdentityResolver[] = [
  codexSessionHeaderResolver,
  anthropicClaudeCodeSessionHeaderResolver
] as const

function collectSessionHeader(
  context: GatewaySessionIdentityResolverContext,
  name: string,
  input: {
    resolverId: string
    semanticNamespace: string
    priority: number
  }
): GatewaySessionIdentityRawCandidate[] {
  return gatewaySessionHeaderValues(context.request, name).map((rawValue) => ({
    resolverId: input.resolverId,
    semanticKind: 'session',
    semanticNamespace: input.semanticNamespace,
    source: { location: 'header', path: name },
    confidence: 'authoritative',
    priority: input.priority,
    rawValue
  }))
}

function isCodexSessionPath(path: string): boolean {
  return path === '/responses' || path === '/responses/compact'
}

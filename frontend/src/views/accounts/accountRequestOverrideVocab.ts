// 账户请求覆盖两个字段（credentials.service_tier_override / reasoning_effort_override）
// 的值域与档位序前端镜像。
//
// 单一真源在后端：backend-go/shared/platform/requestoverride
// （模块 github.com/huanminabc/juhe-ai/backend-go-platform/requestoverride）。
// 账户保存校验与网关运行时降级都以后端为准；本文件只服务前端选项展示，
// 调整族集 / 档位序 / token 形状时必须先改 Go 真源再同步本文件。
//
// 语义（2026-10-18 简化定案）：值域按“协议族”定义，不按账户已选模型或供应商
// 模型目录过滤；模型目录只在运行时决定是否降级。`ultra` 不属于本值域（它是
// Codex 客户端能力，上游 wire 以 `max` 表达）。

export const REQUEST_OVERRIDE_FAMILY_OPENAI = 'openai'
export const REQUEST_OVERRIDE_FAMILY_ANTHROPIC = 'anthropic'
export const REQUEST_OVERRIDE_FAMILY_GEMINI = 'gemini'

export type AccountRequestOverrideFamily = 'openai' | 'anthropic' | 'gemini'

// 各协议族可配置的思考级别全集（档位由低到高），镜像 Go reasoningEffortsByFamily。
export const REQUEST_OVERRIDE_REASONING_EFFORTS_BY_FAMILY: Record<AccountRequestOverrideFamily, string[]> = {
  openai: ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'],
  anthropic: ['low', 'medium', 'high', 'xhigh', 'max'],
  gemini: ['minimal', 'low', 'medium', 'high']
}

// 各协议族可配置的服务等级全集，镜像 Go serviceTiersByFamily。
// Anthropic 不提供账户服务等级选项（其原生 auto/standard_only 不映射为本站通用档位）。
export const REQUEST_OVERRIDE_SERVICE_TIERS_BY_FAMILY: Record<AccountRequestOverrideFamily, string[]> = {
  openai: ['default', 'priority', 'flex'],
  anthropic: [],
  gemini: ['default', 'priority', 'flex']
}

// 跨族统一档位序（由低到高），镜像 Go effortOrder；向下兼容降级由后端在运行时执行。
export const REQUEST_OVERRIDE_EFFORT_ORDER: readonly string[] = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']

// 覆盖字段凭据 token 形状，与 Go requestoverride.TokenPattern 同文本
//（首尾空白即非法，长度上限 64）。
export const REQUEST_OVERRIDE_TOKEN_PATTERN = '^[a-z0-9][a-z0-9._-]{0,63}$'

const requestOverrideTokenRegExp = new RegExp(REQUEST_OVERRIDE_TOKEN_PATTERN, 'i')

export function isAccountRequestOverrideTokenShape(value: string): boolean {
  return requestOverrideTokenRegExp.test(value)
}

// FamilyForProtocol 镜像：协议族优先；空值或未知协议返回空串（由调用方回落供应商族）。
function familyForProtocol(protocolCode: string | undefined): AccountRequestOverrideFamily | '' {
  switch ((protocolCode ?? '').toLowerCase().trim()) {
    case 'anthropic':
    case 'anthropic_messages':
      return REQUEST_OVERRIDE_FAMILY_ANTHROPIC
    case 'gemini':
      return REQUEST_OVERRIDE_FAMILY_GEMINI
    case 'openai':
      return REQUEST_OVERRIDE_FAMILY_OPENAI
    default:
      return ''
  }
}

// FamilyForProvider 镜像：未知供应商回落 openai 族（所有 OpenAI 兼容供应商）。
function familyForProvider(providerCode: string | undefined): AccountRequestOverrideFamily {
  switch ((providerCode ?? '').toLowerCase().trim()) {
    case 'anthropic':
      return REQUEST_OVERRIDE_FAMILY_ANTHROPIC
    case 'gemini':
      return REQUEST_OVERRIDE_FAMILY_GEMINI
    default:
      return REQUEST_OVERRIDE_FAMILY_OPENAI
  }
}

// FamilyOf 镜像：协议族优先，协议码取不到时回落供应商族。
export function accountRequestOverrideFamilyOf(providerCode: string | undefined, protocolCode?: string): AccountRequestOverrideFamily {
  const family = familyForProtocol(protocolCode)
  return family || familyForProvider(providerCode)
}

// 该协议族的思考级别全集（副本，低档在前）。
export function accountRequestOverrideReasoningEfforts(family: AccountRequestOverrideFamily): string[] {
  return [...REQUEST_OVERRIDE_REASONING_EFFORTS_BY_FAMILY[family]]
}

// 该协议族的服务等级全集（副本）。
export function accountRequestOverrideServiceTiers(family: AccountRequestOverrideFamily): string[] {
  return [...REQUEST_OVERRIDE_SERVICE_TIERS_BY_FAMILY[family]]
}

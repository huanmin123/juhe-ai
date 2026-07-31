import type { AccountFormModel } from './accountFormTypes'

const geminiCliOAuthClientId = '681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com'

export function inferGeminiOAuthType(
  credentials: {
    oauth_type?: unknown
    base_url?: unknown
    project_id?: unknown
    client_id?: unknown
  }
): AccountFormModel['oauthType'] {
  const explicit = text(credentials.oauth_type)
  if (explicit === 'code_assist' || explicit === 'google_one' || explicit === 'ai_studio') return explicit
  const baseUrl = text(credentials.base_url)
  if (baseUrl.includes('generativelanguage.googleapis.com')) return 'ai_studio'
  if (text(credentials.project_id) || baseUrl.includes('cloudcode-pa.googleapis.com')) return 'code_assist'
  const clientId = text(credentials.client_id)
  return clientId && clientId !== geminiCliOAuthClientId ? 'ai_studio' : 'code_assist'
}

function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

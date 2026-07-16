import { existsSync, readFileSync } from 'node:fs'

import { parse } from 'dotenv'

export function loadRuntimeBaseEnv(path: string, env: NodeJS.ProcessEnv): Record<string, string> {
  const raw = env.JUHE_AI_DISABLE_BASE_ENV?.trim().toLowerCase()
  if (raw !== undefined && raw !== '' && raw !== 'true' && raw !== 'false') {
    throw new Error('JUHE_AI_DISABLE_BASE_ENV 只能配置为 true 或 false')
  }
  return raw === 'true' ? {} : loadRuntimeEnvFile(path)
}

export function loadRuntimeEnvFile(path: string): Record<string, string> {
  if (!existsSync(path)) return {}
  return parse(readFileSync(path))
}

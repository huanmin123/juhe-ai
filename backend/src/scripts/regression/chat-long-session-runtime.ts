const childProcessEnvAllowlist = [
  'PATH', 'Path', 'PATHEXT', 'SystemRoot', 'SYSTEMROOT', 'ComSpec', 'COMSPEC',
  'TEMP', 'TMP', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'ProgramData',
  'HOMEDRIVE', 'HOMEPATH', 'HOME', 'SHELL', 'PNPM_HOME'
] as const

export function pickChildProcessBaseEnv(env: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const output: NodeJS.ProcessEnv = {}
  for (const key of childProcessEnvAllowlist) {
    if (env[key] !== undefined) output[key] = env[key]
  }
  return output
}

export function redactKnownSecrets(text: string, secrets: readonly (string | undefined)[]): string {
  let redacted = text
  const values = [...new Set(secrets.filter((value): value is string => Boolean(value && value.length >= 3)))].sort((left, right) => right.length - left.length)
  for (const value of values) redacted = redacted.replaceAll(value, '[REDACTED]')
  return redacted
    .replace(/\bBearer\s+[^\s,;"'}]+/gi, 'Bearer [REDACTED]')
    .replace(/\b(authorization|cookie|x-api-key)\b\s*[:=]\s*["']?[^\s,;"'}]+["']?/gi, '$1=[REDACTED]')
    .replace(/(["'](?:authorization|cookie|x-api-key)["']\s*:\s*)["'][^"']*["']/gi, '$1"[REDACTED]"')
}

export function sanitizeErrorForDiagnostics(error: unknown, secrets: readonly (string | undefined)[]): Error {
  const rendered = error instanceof Error
    ? [error.name, error.message, error.stack ?? ''].filter(Boolean).join('\n')
    : inspect(error, { depth: 6, maxArrayLength: 50, maxStringLength: 16_384 })
  return new Error(redactKnownSecrets(rendered, secrets).slice(0, 32 * 1024))
}

export async function runIndependentCleanup(
  primaryError: Error | undefined,
  actions: readonly (() => Promise<void>)[],
  secrets: readonly (string | undefined)[] = []
): Promise<void> {
  const cleanupErrors: Error[] = []
  for (const action of actions) {
    try { await action() } catch (error) { cleanupErrors.push(sanitizeErrorForDiagnostics(error, secrets)) }
  }
  const safePrimaryError = primaryError ? sanitizeErrorForDiagnostics(primaryError, secrets) : undefined
  if (cleanupErrors.length) {
    const errors = safePrimaryError ? [safePrimaryError, ...cleanupErrors] : cleanupErrors
    throw new AggregateError(errors, 'chat_long_session_cleanup_failed', { cause: safePrimaryError ?? cleanupErrors[0] })
  }
  if (safePrimaryError) throw safePrimaryError
}

export async function retryBusyCleanup(
  operation: () => Promise<void>,
  options: {
    maxAttempts: number
    baseDelayMs: number
    maxDelayMs: number
    sleep: (delayMs: number) => Promise<void>
    onBusy?: (input: { attempt: number; error: unknown }) => Promise<unknown> | unknown
  }
): Promise<void> {
  const diagnostics: unknown[] = []
  for (let attempt = 1; attempt <= options.maxAttempts; attempt += 1) {
    try {
      await operation()
      return
    } catch (error) {
      if (!isErrorCode(error, 'EBUSY')) throw error
      if (options.onBusy) {
        try { diagnostics.push(await options.onBusy({ attempt, error })) } catch { diagnostics.push({ attempt, locator: 'unavailable' }) }
      }
      if (attempt >= options.maxAttempts) {
        throw new Error('chat_long_session_temp_cleanup_busy', {
          cause: new Error(`chat_long_session_temp_cleanup_diagnostics:${JSON.stringify(diagnostics).slice(0, 16 * 1024)}`)
        })
      }
      await options.sleep(Math.min(options.maxDelayMs, options.baseDelayMs * (2 ** (attempt - 1))))
    }
  }
}

function isErrorCode(error: unknown, code: string): boolean {
  return Boolean(error && typeof error === 'object' && 'code' in error && (error as { code?: unknown }).code === code)
}
import { inspect } from 'node:util'

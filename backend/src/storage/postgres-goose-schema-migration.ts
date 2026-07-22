import { spawn } from 'node:child_process'
import { access } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

export interface RunGooseSchemaUpOptions {
  postgresURL: string
  maintenanceBinary?: string
  goBackendRoot?: string
  migrationDir?: string
  env?: NodeJS.ProcessEnv
  spawnImpl?: GooseSchemaUpSpawn
}

export interface GooseSchemaUpChildProcess {
  stdout?: { on(event: 'data', listener: (chunk: Buffer | string) => void): unknown }
  stderr?: { on(event: 'data', listener: (chunk: Buffer | string) => void): unknown }
  on(event: 'error', listener: (error: Error) => void): unknown
  on(event: 'close', listener: (code: number | null) => void): unknown
}

export type GooseSchemaUpSpawn = (
  command: string,
  args: readonly string[],
  options: {
    cwd: string
    env: NodeJS.ProcessEnv
    shell: false
    windowsHide: true
  }
) => GooseSchemaUpChildProcess

export async function runGooseSchemaUp(options: RunGooseSchemaUpOptions): Promise<void> {
  const postgresURL = options.postgresURL.trim()
  if (!postgresURL) {
    throw new Error('JUHE_AI_POSTGRES_URL is required for Goose schema-up')
  }

  const goBackendRoot = path.resolve(
    options.goBackendRoot ?? path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../../backend-go')
  )
  const migrationDir = path.resolve(options.migrationDir ?? path.join(goBackendRoot, 'db', 'migrations'))
  const spawnImpl = options.spawnImpl ?? (spawn as unknown as GooseSchemaUpSpawn)
  const env = {
    ...process.env,
    ...options.env,
    JUHE_AI_POSTGRES_URL: postgresURL
  }

  const binary = options.maintenanceBinary?.trim()
  const command = binary && binary.length > 0 ? binary : 'go'
  const args = binary && binary.length > 0
    ? ['schema-up', '--dir', migrationDir]
    : ['run', './cmd/juhe-ai-maintenance', 'schema-up', '--dir', migrationDir]
  const cwd = binary && binary.length > 0 ? process.cwd() : goBackendRoot

  if (!(binary && binary.length > 0)) {
    await access(path.join(goBackendRoot, 'cmd', 'juhe-ai-maintenance', 'main.go'))
    await access(migrationDir)
  }

  const result = await new Promise<{ code: number | null; stdout: string; stderr: string }>((resolve, reject) => {
    const child = spawnImpl(command, args, {
      cwd,
      env,
      shell: false,
      windowsHide: true
    })
    let stdout = ''
    let stderr = ''
    child.stdout?.on('data', (chunk: Buffer | string) => {
      stdout += String(chunk)
    })
    child.stderr?.on('data', (chunk: Buffer | string) => {
      stderr += String(chunk)
    })
    child.on('error', reject)
    child.on('close', code => resolve({ code, stdout, stderr }))
  })

  if (result.code !== 0) {
    const detail = [result.stderr.trim(), result.stdout.trim()].filter(Boolean).join('\n')
    throw new Error(detail || `Goose schema-up failed with exit code ${String(result.code)}`)
  }
}

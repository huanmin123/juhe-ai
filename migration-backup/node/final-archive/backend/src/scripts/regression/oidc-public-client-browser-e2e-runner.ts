import assert from 'node:assert/strict'
import { spawn, type ChildProcess } from 'node:child_process'
import { once } from 'node:events'
import { existsSync, mkdtempSync, rmSync } from 'node:fs'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const temporaryRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-oidc-external-client-'))
const readyFile = join(temporaryRoot, 'ready.json')
let child: ChildProcess | undefined

try {
  const providerPort = await reserveIpv6LoopbackPort()
  child = spawn(process.execPath, [
    '--import',
    'tsx',
    fileURLToPath(new URL('./oidc-public-client-browser-e2e.ts', import.meta.url))
  ], {
    cwd: process.cwd(),
    env: isolatedEnvironment(temporaryRoot, readyFile, providerPort),
    stdio: ['ignore', 'pipe', 'inherit']
  })
  const childExit = once(child, 'exit') as Promise<[number | null, NodeJS.Signals | null]>

  const startUrl = await waitForReadyOutputOrChildExit(child)
  if (!URL.canParse(startUrl)) {
    throw new Error('外部 Client 未写出有效启动地址')
  }
  process.stdout.write(`OIDC external client ready: ${startUrl}\n`)
  if (process.env.JUHE_AI_EXTERNAL_CLIENT_E2E_AUTOMATE_HTTP === '1') {
    await completeAuthorizationWithHttpClient(startUrl)
  }

  const [exitCode, signal] = await childExit
  if (exitCode !== 0) {
    throw new Error(`外部 Client E2E 子进程异常退出：code=${exitCode ?? 'null'} signal=${signal ?? 'none'}`)
  }
  process.stdout.write('oidc-public-client-browser-e2e: passed\n')
} finally {
  await stopChild(child)
  rmSync(temporaryRoot, { recursive: true, force: true })
  if (existsSync(temporaryRoot)) {
    throw new Error('外部 Client E2E 临时目录未清理')
  }
}

function isolatedEnvironment(root: string, readyPath: string, providerPort: number): NodeJS.ProcessEnv {
  return {
    ...process.env,
    NODE_ENV: 'test',
    JUHE_AI_EXTERNAL_CLIENT_E2E_CHILD: '1',
    JUHE_AI_EXTERNAL_CLIENT_E2E_TEMP_ROOT: root,
    JUHE_AI_EXTERNAL_CLIENT_E2E_PROVIDER_PORT: String(providerPort),
    JUHE_AI_EXTERNAL_CLIENT_E2E_READY_FILE: readyPath,
    JUHE_AI_PROCESS_ROLE: 'db-service',
    JUHE_AI_DATABASE_DRIVER: 'sqlite',
    JUHE_AI_SQLITE_READ_WORKER_POOL_SIZE: '0',
    JUHE_AI_AUTH_CAPTCHA_DISABLED: 'true',
    JUHE_AI_OIDC_ENABLED: 'true',
    JUHE_AI_OIDC_ISSUER: `http://[::1]:${providerPort}`,
    JUHE_AI_OIDC_KEY_ENCRYPTION_SECRET: 'external-client-e2e-key-encryption-secret',
    JUHE_AI_DATABASE_PATH: join(root, 'business.sqlite3'),
    JUHE_AI_CHAT_DATABASE_PATH: join(root, 'chat.sqlite3'),
    JUHE_AI_DATASET_DATABASE_PATH: join(root, 'dataset.sqlite3'),
    JUHE_AI_RUNTIME_LOG_DATABASE_PATH: join(root, 'runtime-log.sqlite3'),
    JUHE_AI_TABLE_MONITOR_DATABASE_PATH: join(root, 'table-monitor.sqlite3'),
    JUHE_AI_USAGE_CATALOG_DATABASE_PATH: join(root, 'usage-catalog.sqlite3'),
    JUHE_AI_STATS_DATABASE_PATH: join(root, 'stats.sqlite3'),
    JUHE_AI_AUDIT_LOG_DATABASE_PATH: join(root, 'audit-log.sqlite3'),
    JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY: join(root, 'audit-blobs'),
    JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY: join(root, 'audit-hot-search'),
    JUHE_AI_LOG_DIR: join(root, 'logs'),
    JUHE_AI_USAGE_SHARD_ROOT: join(root, 'usage-shards'),
    JUHE_AI_CODEX_CONTEXT_ROOT: join(root, 'codex-context'),
    JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT: join(root, 'codex-context-state'),
    JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
    JUHE_AI_LOG_FILE_ENABLED: 'false',
    JUHE_AI_AUDIT_LOG_ENABLED: 'false',
    JUHE_AI_DEV_AUTO_LOGIN_USERNAME: ''
  }
}

async function reserveIpv6LoopbackPort(): Promise<number> {
  const server = createServer()
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '::1', () => resolve())
  })
  try {
    const address = server.address()
    if (!address || typeof address === 'string') throw new Error('无法取得 IPv6 loopback 测试端口')
    return address.port
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
}

async function waitForReadyOutputOrChildExit(childProcess: ChildProcess): Promise<string> {
  const childStdout = childProcess.stdout
  if (!childStdout) throw new Error('外部 Client 子进程没有标准输出管道')
  return await new Promise<string>((resolve, reject) => {
    let pendingOutput = ''
    const childExitHandler = (code: number | null, signal: NodeJS.Signals | null) => {
      complete(() => reject(new Error(`外部 Client 在就绪前退出：code=${code ?? 'null'} signal=${signal ?? 'none'}`)))
    }
    const childOutputHandler = (chunk: Buffer) => {
      pendingOutput += chunk.toString('utf8')
      const lines = pendingOutput.split(/\r?\n/)
      pendingOutput = lines.pop() ?? ''
      for (const line of lines) {
        if (line.startsWith('E2E_READY ')) {
          const startUrl = line.slice('E2E_READY '.length).trim()
          if (!URL.canParse(startUrl)) {
            complete(() => reject(new Error('外部 Client 输出了无效就绪地址')))
            return
          }
          complete(() => resolve(startUrl))
          return
        }
        process.stdout.write(`${line}\n`)
      }
    }
    const complete = (settle: () => void): void => {
      childProcess.off('exit', childExitHandler)
      childStdout.off('data', childOutputHandler)
      settle()
    }
    childProcess.once('exit', childExitHandler)
    childStdout.on('data', childOutputHandler)
  })
}

async function stopChild(childProcess: ChildProcess | undefined): Promise<void> {
  if (!childProcess || childProcess.exitCode !== null || childProcess.killed) return
  childProcess.kill()
  await once(childProcess, 'exit')
}

async function completeAuthorizationWithHttpClient(startUrl: string): Promise<void> {
  const startResponse = await fetch(startUrl, { redirect: 'manual' })
  assert.equal(startResponse.status, 302, '外部 Client 启动必须跳转到 Provider')
  const loginUrl = requiredRedirectLocation(startResponse, '外部 Client 启动缺少登录桥接地址')

  const loginResponse = await fetch(loginUrl, { redirect: 'manual' })
  assert.equal(loginResponse.status, 302, '登录桥接必须跳转到授权确认页')
  const sessionCookie = loginResponse.headers.get('set-cookie')
  if (!sessionCookie?.startsWith('juhe_ai_session=')) {
    throw new Error('登录桥接必须只设置 Provider 会话 Cookie')
  }
  const providerSessionCookie = sessionCookie.split(';', 1)[0]
  const authorizeUrl = requiredRedirectLocation(loginResponse, '登录桥接缺少授权确认地址')

  const authorizeResponse = await fetch(authorizeUrl, {
    headers: { cookie: providerSessionCookie }
  })
  assert.equal(authorizeResponse.status, 200, '授权确认页必须正常返回')
  const authorizeHtml = await authorizeResponse.text()
  const transactionId = requiredFormValue(authorizeHtml, 'transaction_id')
  const csrfToken = requiredFormValue(authorizeHtml, 'csrf_token')
  const decisionResponse = await fetch(new URL('/oauth/authorize/decision', authorizeUrl), {
    method: 'POST',
    redirect: 'manual',
    headers: {
      'content-type': 'application/x-www-form-urlencoded',
      cookie: providerSessionCookie
    },
    body: new URLSearchParams({ transaction_id: transactionId, csrf_token: csrfToken, decision: 'allow' })
  })
  assert.equal(decisionResponse.status, 302, '允许授权必须回调外部 Client')
  const callbackUrl = requiredRedirectLocation(decisionResponse, '允许授权缺少回调地址')
  const callbackResponse = await fetch(callbackUrl, { redirect: 'manual' })
  assert.equal(callbackResponse.status, 200, '外部 Client 回调处理失败')
  assert.match(await callbackResponse.text(), /外部 Client 验证成功/, '外部 Client 未确认读取身份和限额数据')
}

function requiredRedirectLocation(response: Response, message: string): string {
  const location = response.headers.get('location')
  assert(location && URL.canParse(location), message)
  return location
}

function requiredFormValue(html: string, name: string): string {
  const escapedName = name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const match = new RegExp(`<input[^>]+name="${escapedName}"[^>]+value="([^"]+)"`, 'i').exec(html)
  assert(match?.[1], `授权确认页缺少 ${name}`)
  return match[1]
}

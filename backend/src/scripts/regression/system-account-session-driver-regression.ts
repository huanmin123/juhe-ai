import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

if (process.env.JUHE_SYSTEM_ACCOUNT_SESSION_DRIVER_CHILD === 'postgres') {
  const repositories = await import('../../storage/repositories.js')
  await assertSystemAccountSessionAsync(repositories)
  process.exit(0)
}

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-system-session-driver-'))
try {
  process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
  process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
  process.env.JUHE_AI_CACHE_DRIVER = 'memory'
  process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'
  process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
  process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
  process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = join(tempRoot, 'usage-catalog.sqlite3')
  process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
  process.env.JUHE_AI_USAGE_SHARD_ROOT = join(tempRoot, 'usage-shards')
  process.env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = join(tempRoot, 'codex-context')

  const repositories = await import('../../storage/repositories.js')
  await assertSystemAccountSessionAsync(repositories)

  if (process.env.JUHE_SYSTEM_ACCOUNT_SESSION_POSTGRES_URL) {
    const result = spawnSync(process.execPath, [
      '--import',
      'tsx',
      fileURLToPath(import.meta.url)
    ], {
      cwd: process.cwd(),
      encoding: 'utf8',
      env: {
        ...process.env,
        JUHE_SYSTEM_ACCOUNT_SESSION_DRIVER_CHILD: 'postgres',
        JUHE_AI_RUNTIME_MODE: 'performance',
        JUHE_AI_DATABASE_DRIVER: 'postgres',
        JUHE_AI_CACHE_DRIVER: 'redis',
        JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
        JUHE_AI_QUEUE_DRIVER: 'redis_stream',
        JUHE_AI_POSTGRES_URL: process.env.JUHE_SYSTEM_ACCOUNT_SESSION_POSTGRES_URL,
        JUHE_AI_REDIS_CACHE_URL: process.env.JUHE_SYSTEM_ACCOUNT_SESSION_REDIS_CACHE_URL ?? 'redis://:unused@127.0.0.1:6379/0',
        JUHE_AI_REDIS_STATE_URL: process.env.JUHE_SYSTEM_ACCOUNT_SESSION_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0',
        JUHE_AI_REDIS_QUEUE_URL: process.env.JUHE_SYSTEM_ACCOUNT_SESSION_REDIS_QUEUE_URL ?? process.env.JUHE_SYSTEM_ACCOUNT_SESSION_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0'
      }
    })
    if (result.status !== 0) {
      process.stdout.write(result.stdout)
      process.stderr.write(result.stderr)
      process.exit(result.status ?? 1)
    }
  }

  console.log('system-account-session-driver-regression passed')
} finally {
  await closeSqliteStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertSystemAccountSessionAsync(repositories: typeof import('../../storage/repositories.js')): Promise<void> {
  const missingAccount = await repositories.verifySystemAccountCredentialsAsync('admin', 'wrong-password')
  assert.equal(missingAccount, undefined, '错误密码不能通过校验')

  const account = await repositories.verifySystemAccountCredentialsAsync('admin', 'admin')
  assert.ok(account, '默认管理员应能通过异步凭据校验')
  assert.equal(account.id, 'sys_admin')
  assert.equal(account.status, 'active')
  assert.equal(account.mustChangePassword, false, '默认管理员不应被强制初始改密')

  const byId = await repositories.findSystemAccountByIdAsync(account.id)
  assert.equal(byId?.username, 'admin', '应能按 ID 异步读取系统账户')

  const byUsername = await repositories.findSystemAccountByUsernameAsync('ADMIN')
  assert.equal(byUsername?.id, account.id, '用户名异步读取应大小写不敏感')
  assert.ok(byUsername?.passwordHash, '异步用户名读取应返回密码 hash 供校验')

  const session = await repositories.createSessionAsync(account.id, 1)
  assert.ok(session.token, '应创建 session token')
  assert.ok(session.sessionId.startsWith('sess_'), 'session id 应使用 sess 前缀')

  const loadedSession = await repositories.findSessionByTokenAsync(session.token)
  assert.equal(loadedSession?.sessionId, session.sessionId, '应能按 token 异步读取 session')
  assert.equal(loadedSession?.account.id, account.id, 'session 应水合系统账户')

  await sleep(5)
  await repositories.touchSessionAsync(session.sessionId, '2000-01-01T00:00:00.000Z')
  const touchedSession = await repositories.findSessionByTokenAsync(session.token)
  assert.ok(touchedSession, 'touch 后 session 应仍可读取')
  assert.equal(touchedSession.sessionId, loadedSession?.sessionId, 'touch 不应改变 session 身份')

  const otherSession = await repositories.createSessionAsync(account.id, 1)
  await repositories.revokeOtherSessionsForAccountAsync(account.id, otherSession.sessionId)
  assert.equal(await repositories.findSessionByTokenAsync(session.token), undefined, '撤销其他 session 后旧 session 不应可用')
  assert.ok(await repositories.findSessionByTokenAsync(otherSession.token), '保留的 session 应仍可用')

  await repositories.revokeSessionAsync(otherSession.token)
  assert.equal(await repositories.findSessionByTokenAsync(otherSession.token), undefined, '撤销 session 后 token 不应可用')

  const finalSession = await repositories.createSessionAsync(account.id, 1)
  await repositories.revokeAllSessionsForAccountAsync(account.id)
  assert.equal(await repositories.findSessionByTokenAsync(finalSession.token), undefined, '撤销账户全部 session 后 token 不应可用')

  await repositories.updateSystemAccountLastLoginAsync(account.id)
  const accountAfterLogin = await repositories.findSystemAccountByIdAsync(account.id)
  assert.ok(accountAfterLogin?.lastLoginAt, '更新最近登录时间后应能读取 lastLoginAt')
}

async function closeSqliteStorageDatabases(): Promise<void> {
  try {
    const databaseModule = await import('../../storage/database.js')
    databaseModule.closeStorageDatabases()
  } catch {
    // The regression may fail before SQLite storage is imported.
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

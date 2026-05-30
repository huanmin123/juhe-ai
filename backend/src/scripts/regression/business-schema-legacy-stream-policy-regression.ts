import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { applyBusinessSchema } from '../../storage/schema.js'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-business-schema-'))

try {
  const database = new DatabaseSync(join(tempRoot, 'business.sqlite3'))
  try {
    database.exec(`
      CREATE TABLE stream_intercept_policies (
        id TEXT PRIMARY KEY,
        name TEXT NOT NULL,
        enabled INTEGER NOT NULL DEFAULT 1,
        execution_mode TEXT NOT NULL DEFAULT 'intercept',
        priority INTEGER NOT NULL DEFAULT 100,
        scope_level TEXT NOT NULL DEFAULT 'global',
        scope_json TEXT NOT NULL DEFAULT '{}',
        match_json TEXT NOT NULL DEFAULT '{}',
        data_handling TEXT NOT NULL DEFAULT 'discard_stream',
        retry_enabled INTEGER NOT NULL DEFAULT 0,
        account_switch TEXT NOT NULL DEFAULT 'none',
        account_state TEXT NOT NULL DEFAULT 'none',
        avoidance_ttl_seconds INTEGER,
        notes TEXT,
        created_at TEXT NOT NULL,
        updated_at TEXT NOT NULL
      );

      INSERT INTO stream_intercept_policies (
        id, name, enabled, execution_mode, priority, scope_level, scope_json, match_json,
        data_handling, retry_enabled, account_switch, account_state, created_at, updated_at
      )
      VALUES (
        'sip_legacy', '旧版拦截策略', 1, 'intercept', 100, 'global', '{}', '{}',
        'discard_stream', 0, 'none', 'none', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z'
      );
    `)

    applyBusinessSchema(database)

    const columns = database
      .prepare('PRAGMA table_info(stream_intercept_policies)')
      .all() as Array<{ name?: string }>
    assert(columns.some((column) => column.name === 'provider_code'), '旧版流拦截策略表应补齐 provider_code 列')
    assert(columns.some((column) => column.name === 'action'), '旧版流拦截策略表应补齐 action 模板列')

    const row = database
      .prepare('SELECT provider_code, action FROM stream_intercept_policies WHERE id = ?')
      .get('sip_legacy') as { provider_code?: string; action?: string } | undefined
    assert.equal(row?.provider_code, 'openai', '旧版流拦截策略应回填默认供应商 openai')
    assert.equal(row?.action, 'avoid_account_ttl', '旧版流拦截策略应回填默认 action 模板')

    const index = database
      .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_stream_intercept_policies_provider_priority'")
      .get() as { name?: string } | undefined
    assert.equal(index?.name, 'idx_stream_intercept_policies_provider_priority', '补列后应创建 provider_code 索引')
  } finally {
    database.close()
  }
} finally {
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('业务 schema 旧版流拦截策略迁移回归通过：provider_code 与 action 先补列再建索引')

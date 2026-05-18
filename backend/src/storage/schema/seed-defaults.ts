import type { DatabaseSync } from 'node:sqlite'

import { hashPassword } from '../crypto.js'
import { DEFAULT_GLOBAL_SETTINGS, DEFAULT_OPENAI_GROUP, DEFAULT_SYSTEM_SETTINGS, OPENAI_PROVIDER_SEED } from '../schema-defaults.js'

export function seedDefaults(database: DatabaseSync): void {
  const now = new Date().toISOString()

  database
    .prepare(`
      INSERT OR IGNORE INTO system_accounts (
        id, username, display_name, description, role, status, password_hash, must_change_password, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      'sys_admin',
      'admin',
      '管理员',
      '系统默认管理员账户',
      'admin',
      'active',
      hashPassword('admin'),
      1,
      now,
      now
    )

  const globalStatement = database.prepare(`
    INSERT OR IGNORE INTO global_settings (key, value_json, updated_at)
    VALUES (?, ?, ?)
  `)
  for (const [key, value] of DEFAULT_GLOBAL_SETTINGS) {
    globalStatement.run(key, JSON.stringify(value), now)
  }

  database
    .prepare(`
      INSERT OR IGNORE INTO providers (
        id, code, name, description, enabled, base_url, account_types_json, capabilities_json, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      OPENAI_PROVIDER_SEED.id,
      OPENAI_PROVIDER_SEED.code,
      OPENAI_PROVIDER_SEED.name,
      OPENAI_PROVIDER_SEED.description,
      OPENAI_PROVIDER_SEED.enabled,
      OPENAI_PROVIDER_SEED.baseUrl,
      JSON.stringify(OPENAI_PROVIDER_SEED.accountTypes),
      JSON.stringify(OPENAI_PROVIDER_SEED.capabilities),
      now,
      now
    )

  seedAdminDefaultOpenAIGroup(database, now)

  const statement = database.prepare(`
    INSERT OR IGNORE INTO system_settings (system_account_id, key, value_json, updated_at)
    VALUES (?, ?, ?, ?)
  `)

  for (const [key, value] of DEFAULT_SYSTEM_SETTINGS) {
    statement.run('sys_admin', key, JSON.stringify(value), now)
  }
}

function seedAdminDefaultOpenAIGroup(database: DatabaseSync, timestamp: string): void {
  database
    .prepare(`
      INSERT OR IGNORE INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)
    `)
    .run(
      DEFAULT_OPENAI_GROUP.id,
      DEFAULT_OPENAI_GROUP.systemAccountId,
      DEFAULT_OPENAI_GROUP.name,
      DEFAULT_OPENAI_GROUP.providerCode,
      DEFAULT_OPENAI_GROUP.description,
      timestamp,
      timestamp
    )

  database
    .prepare('UPDATE groups SET is_default = 1 WHERE id = ? AND system_account_id = ?')
    .run(DEFAULT_OPENAI_GROUP.id, DEFAULT_OPENAI_GROUP.systemAccountId)
}

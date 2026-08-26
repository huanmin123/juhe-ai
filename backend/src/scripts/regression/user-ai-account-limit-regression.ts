import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const source = (path: string) => readFileSync(resolve(path), 'utf8')

const accountWriteSource = source('src/storage/repositories.ts')
const systemAccountSource = source('src/storage/system-accounts.repository.ts')
const settingsSource = source('src/storage/settings.repository.ts')
const defaultsSource = source('src/storage/schema-defaults.ts')
const businessSchemaSource = source('src/storage/schema/business-schema.ts')
const postgresSchemaSource = source('src/storage/postgres-schema.ts')

assert.match(defaultsSource, /\['userAiAccountLimit', 100\]/, '全局 AI 账户数量限制默认值必须为 100')
assert.match(settingsSource, /userAiAccountLimit: integerSetting\(0, 1_000_000\)/, '全局 AI 账户数量限制必须校验为非负整数')
assert.match(settingsSource, /'user-request-limit': \{[\s\S]*'userAiAccountLimit'/, '用户限制设置分区必须包含 AI 账户数量限制')
assert.match(businessSchemaSource, /ai_account_limit INTEGER/, 'SQLite system_accounts 必须存储 AI 账户数量覆盖值')
assert.match(postgresSchemaSource, /system-account-ai-account-limit-pg-column/, 'PostgreSQL 既有库必须补充 AI 账户数量覆盖列')
assert.match(systemAccountSource, /aiAccountLimit: normalizeAiAccountLimit\(input\.aiAccountLimit\)/, '系统账户创建必须持久化 AI 账户数量覆盖')
assert.match(systemAccountSource, /aiAccountLimit: Object\.prototype\.hasOwnProperty\.call\(input, 'aiAccountLimit'\)/, '系统账户更新必须支持清空 AI 账户数量覆盖')
assert.match(accountWriteSource, /assertAiAccountCreationLimitInSqliteTransaction\(database, systemAccountId\)/, 'SQLite 账户创建必须校验 AI 账户数量限制')
assert.match(accountWriteSource, /await assertAiAccountCreationLimitInClientTransaction\(client, systemAccountId\)/, '异步账户创建必须校验 AI 账户数量限制')
assert.match(accountWriteSource, /authorization_instance_authorization_id IS NULL/, 'AI 账户数量限制不得计入授权实例')
assert.match(accountWriteSource, /deleted_at IS NULL/, 'AI 账户数量限制不得计入已删除账户')
assert.match(accountWriteSource, /LIMIT 1\$\{lockClause\}/, 'PostgreSQL 创建校验必须锁定所属系统账户行')

console.log('USER_AI_ACCOUNT_LIMIT_REGRESSION_OK')

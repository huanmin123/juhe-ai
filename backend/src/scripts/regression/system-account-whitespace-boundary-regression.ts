import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-account-whitespace-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'system-account-whitespace-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, crypto] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/crypto.js')
])

try {
  assert.throws(
    () => repositories.createSystemAccount({
      username: 'bad user',
      displayName: 'baduser',
      password: 'password',
      role: 'user',
      status: 'active',
      mustChangePassword: false
    }),
    /用户账户不能包含空格/,
    '创建系统账户时用户名不能包含空格'
  )

  assert.throws(
    () => repositories.createSystemAccount({
      username: 'bad_display_user',
      displayName: 'bad user',
      password: 'password',
      role: 'user',
      status: 'active',
      mustChangePassword: false
    }),
    /用户名称不能包含空格/,
    '创建系统账户时用户名称不能包含空格'
  )

  assert.throws(
    () => repositories.createSystemAccount({
      username: 'bad_password_user',
      displayName: 'badpassworduser',
      password: 'pass word',
      role: 'user',
      status: 'active',
      mustChangePassword: false
    }),
    /登录密码不能包含空格/,
    '创建系统账户时登录密码不能包含空格'
  )

  assert.throws(
    () => repositories.createSystemAccount({
      username: 'leading_space_user',
      displayName: ' leading_space_user',
      password: 'password',
      role: 'user',
      status: 'active',
      mustChangePassword: false
    }),
    /用户名称不能包含空格/,
    '创建系统账户时用户名称首尾空格也应拒绝'
  )

  assert.throws(
    () => repositories.createSystemAccountWithPasswordHash({
      username: 'hash_password_bad',
      displayName: 'hashpasswordbad',
      password: 'pass word',
      role: 'user',
      status: 'active',
      mustChangePassword: false
    }, crypto.hashPassword('pass word')),
    /登录密码不能包含空格/,
    '预哈希创建系统账户时也应校验原始登录密码'
  )

  const validUser = repositories.createSystemAccount({
    username: 'whitespace_boundary_user',
    displayName: '空格边界用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })

  assert.equal(validUser.username, 'whitespace_boundary_user', '不含空格的用户名应允许创建')
  assert.equal(validUser.displayName, '空格边界用户', '不含空格的用户名称应允许创建')
  assert(repositories.verifySystemAccountCredentials('whitespace_boundary_user', 'password'), '不含空格的密码应可正常登录校验')

  assert.throws(
    () => repositories.updateSystemAccount(validUser.id, { displayName: 'bad user' }),
    /用户名称不能包含空格/,
    '编辑系统账户时用户名称不能包含空格'
  )

  assert.throws(
    () => repositories.updateSystemAccount(validUser.id, { displayName: '空格边界用户 ' }),
    /用户名称不能包含空格/,
    '编辑系统账户时用户名称尾部空格也应拒绝'
  )

  assert.throws(
    () => repositories.updateSystemAccount(validUser.id, { password: 'pass word' }),
    /登录密码不能包含空格/,
    '重置系统账户密码时登录密码不能包含空格'
  )

  repositories.updateSystemAccount(validUser.id, { displayName: '空格边界用户已更新', password: 'password2' })
  assert(repositories.verifySystemAccountCredentials('whitespace_boundary_user', 'password2'), '不含空格的编辑和重置密码应正常生效')

  console.log('系统账户空格边界回归通过：用户名、用户名称和登录密码在创建、预哈希创建、编辑和重置密码时都拒绝空白字符')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

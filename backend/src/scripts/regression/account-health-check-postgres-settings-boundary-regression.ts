import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { normalizedHealthCheckSettings } from '../../storage/account-health-check.repository.js'

const originalDatabaseDriver = runtimeConfig.databaseDriver

try {
  runtimeConfig.databaseDriver = 'postgres'

  assert.deepEqual(
    normalizedHealthCheckSettings({
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 3
    }),
    {
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 3
    },
    'PG 账号健康检测已传完整设置时不得同步读取本地 settings'
  )

  assert.throws(
    () => normalizedHealthCheckSettings({ intervalHours: 12, jitterMinutes: 0 }),
    /PostgreSQL 账号健康检测必须由调用方显式传入系统设置/,
    'PG 账号健康检测缺设置时必须 fail-fast，禁止回退同步 settings'
  )

  console.log('account-health-check-postgres-settings-boundary-regression passed')
} finally {
  runtimeConfig.databaseDriver = originalDatabaseDriver
}

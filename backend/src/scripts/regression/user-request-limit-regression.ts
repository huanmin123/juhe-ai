import assert from 'node:assert/strict'
import { DatabaseSync } from 'node:sqlite'

import { normalizeUserRequestLimits, resolveEffectiveUserRequestLimits } from '../../domain/user-request-limits.js'
import { UserRequestLimitCounter } from '../../modules/gateway/runtime/user-request-limit-counter.js'
import { applyBusinessSchema } from '../../storage/schema/business-schema.js'

const legacyDatabase = new DatabaseSync(':memory:')
legacyDatabase.exec(`
  CREATE TABLE system_accounts (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    description TEXT,
    role TEXT NOT NULL DEFAULT 'user',
    status TEXT NOT NULL DEFAULT 'active',
    password_hash TEXT NOT NULL,
    must_change_password INTEGER NOT NULL DEFAULT 0,
    image_generation_enabled INTEGER NOT NULL DEFAULT 0,
    last_login_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
  )
`)
applyBusinessSchema(legacyDatabase)
const legacyColumns = legacyDatabase.prepare('PRAGMA table_info(system_accounts)').all() as Array<{ name: string }>
assert(legacyColumns.some((column) => column.name === 'request_limits_json'), 'SQLite 旧库升级必须补齐 request_limits_json')
legacyDatabase.close()

const settings = {
  gatewayUserRequestLimitPerMinute: 2,
  gatewayUserRequestLimitPerDay: 0,
  gatewayUserRequestLimitPerWeek: 0,
  gatewayUserRequestLimitPerMonth: 0,
  usageStatsTimezone: 'Asia/Shanghai'
}

const effective = resolveEffectiveUserRequestLimits({
  ...settings,
  gatewayUserRequestLimitPerDay: 100,
  gatewayUserRequestLimitPerWeek: 700,
  gatewayUserRequestLimitPerMonth: 3_000
}, { perMinute: 0, perWeek: 900 })
assert.deepEqual(effective, {
  perMinute: { limit: 0, source: 'user' },
  perDay: { limit: 100, source: 'global' },
  perWeek: { limit: 900, source: 'user' },
  perMonth: { limit: 3_000, source: 'global' },
  timezone: 'Asia/Shanghai',
  overrideActive: true
})
assert.deepEqual(normalizeUserRequestLimits({ perMinute: 0, perMonth: 99 }), { perMinute: 0, perMonth: 99 })
assert.deepEqual(normalizeUserRequestLimits({ perMinute: 5, expiresOn: '2026-07-26' }), { perMinute: 5, expiresOn: '2026-07-26' })
assert.equal(normalizeUserRequestLimits({}), undefined)
assert.throws(() => normalizeUserRequestLimits({ perDay: -1 }), /perDay/)
assert.throws(() => normalizeUserRequestLimits({ perWeek: 1.5 }), /perWeek/)
assert.throws(() => normalizeUserRequestLimits({ perMonth: 1_000_000_001 }), /perMonth/)
assert.throws(() => normalizeUserRequestLimits({ perMinute: 1, expiresOn: '2026-02-30' }), /expiresOn/)

const counter = new UserRequestLimitCounter()
const firstMinute = Date.parse('2026-07-26T15:59:30.000Z')
assert.equal(counter.consume({ systemAccountId: 'user-a', settings, nowMs: firstMinute }).allowed, true)
assert.equal(counter.consume({ systemAccountId: 'user-a', settings, nowMs: firstMinute }).allowed, true)
const blocked = counter.consume({ systemAccountId: 'user-a', settings, nowMs: firstMinute })
assert.equal(blocked.allowed, false)
assert.equal(blocked.window, 'perMinute')
assert.equal(blocked.limit, 2)
assert.equal(blocked.retryAfterSeconds, 30)
assert.equal(counter.dirtySnapshot().find((entry) => entry.systemAccountId === 'user-a')?.localCount, 3)
assert.equal(counter.consume({ systemAccountId: 'user-b', settings, nowMs: firstMinute }).allowed, true)
assert.equal(counter.consume({ systemAccountId: 'user-a', settings, overrides: { perMinute: 0 }, nowMs: firstMinute }).allowed, true)
assert.equal(counter.consume({ systemAccountId: 'user-a', settings, nowMs: firstMinute + 60_000 }).allowed, true)

const expiringCounter = new UserRequestLimitCounter()
const expiringOverrides = { perMinute: 1, expiresOn: '2026-07-26' }
assert.equal(expiringCounter.consume({ systemAccountId: 'expiring-user', settings, overrides: expiringOverrides, nowMs: firstMinute }).allowed, true)
assert.equal(expiringCounter.consume({ systemAccountId: 'expiring-user', settings, overrides: expiringOverrides, nowMs: firstMinute }).allowed, false)
assert.equal(expiringCounter.consume({ systemAccountId: 'expiring-user', settings, overrides: expiringOverrides, nowMs: firstMinute + 60_000 }).allowed, true)
assert.deepEqual(resolveEffectiveUserRequestLimits(settings, expiringOverrides, firstMinute + 60_000), {
  perMinute: { limit: 2, source: 'global' },
  perDay: { limit: 0, source: 'global' },
  perWeek: { limit: 0, source: 'global' },
  perMonth: { limit: 0, source: 'global' },
  timezone: 'Asia/Shanghai',
  overrideExpiresOn: '2026-07-26',
  overrideActive: false
})

const unlimitedCounter = new UserRequestLimitCounter()
assert.equal(unlimitedCounter.consume({
  systemAccountId: 'unlimited-user',
  settings: { ...settings, gatewayUserRequestLimitPerMinute: 0 }
}).allowed, true)
assert.equal(unlimitedCounter.size(), 0)

const dayCounter = new UserRequestLimitCounter()
const dailySettings = { ...settings, gatewayUserRequestLimitPerMinute: 0, gatewayUserRequestLimitPerDay: 1 }
assert.equal(dayCounter.consume({ systemAccountId: 'day-user', settings: dailySettings, nowMs: Date.parse('2026-07-26T15:59:59.999Z') }).allowed, true)
assert.equal(dayCounter.consume({ systemAccountId: 'day-user', settings: dailySettings, nowMs: Date.parse('2026-07-26T15:59:59.999Z') }).allowed, false)
assert.equal(dayCounter.consume({ systemAccountId: 'day-user', settings: dailySettings, nowMs: Date.parse('2026-07-26T16:00:00.000Z') }).allowed, true)

const weekCounter = new UserRequestLimitCounter()
const weeklySettings = { ...settings, gatewayUserRequestLimitPerMinute: 0, gatewayUserRequestLimitPerWeek: 1 }
assert.equal(weekCounter.consume({ systemAccountId: 'week-user', settings: weeklySettings, nowMs: Date.parse('2026-07-26T15:59:59.999Z') }).allowed, true)
assert.equal(weekCounter.consume({ systemAccountId: 'week-user', settings: weeklySettings, nowMs: Date.parse('2026-07-26T15:59:59.999Z') }).allowed, false)
assert.equal(weekCounter.consume({ systemAccountId: 'week-user', settings: weeklySettings, nowMs: Date.parse('2026-07-26T16:00:00.000Z') }).allowed, true)

const monthCounter = new UserRequestLimitCounter()
const monthlySettings = { ...settings, gatewayUserRequestLimitPerMinute: 0, gatewayUserRequestLimitPerMonth: 1 }
assert.equal(monthCounter.consume({ systemAccountId: 'month-user', settings: monthlySettings, nowMs: Date.parse('2026-07-31T15:59:59.999Z') }).allowed, true)
assert.equal(monthCounter.consume({ systemAccountId: 'month-user', settings: monthlySettings, nowMs: Date.parse('2026-07-31T15:59:59.999Z') }).allowed, false)
assert.equal(monthCounter.consume({ systemAccountId: 'month-user', settings: monthlySettings, nowMs: Date.parse('2026-07-31T16:00:00.000Z') }).allowed, true)

const dirty = counter.dirtySnapshot(2)
assert.equal(dirty.length, 2)
counter.applySyncResults(dirty.map((entry) => ({
  entryKey: entry.entryKey,
  sentLocalCount: entry.localCount,
  remoteTotal: entry.localCount
})))

const coordinationCounter = new UserRequestLimitCounter()
const coordinatedSettings = { ...settings, gatewayUserRequestLimitPerMinute: 10 }
for (let index = 0; index < 6; index += 1) {
  assert.equal(coordinationCounter.consume({ systemAccountId: 'coordinated-user', settings: coordinatedSettings, nowMs: firstMinute }).allowed, true)
}
const coordinatedSnapshot = coordinationCounter.dirtySnapshot(1)[0]
assert.ok(coordinatedSnapshot)
coordinationCounter.applySyncResults([{
  entryKey: coordinatedSnapshot.entryKey,
  sentLocalCount: coordinatedSnapshot.localCount,
  remoteTotal: 12
}])
assert.equal(coordinationCounter.consume({ systemAccountId: 'coordinated-user', settings: coordinatedSettings, nowMs: firstMinute }).allowed, false)

const inFlightCounter = new UserRequestLimitCounter()
assert.equal(inFlightCounter.consume({ systemAccountId: 'in-flight-user', settings: coordinatedSettings, nowMs: firstMinute }).allowed, true)
const inFlightSnapshot = inFlightCounter.dirtySnapshot(1)[0]
assert.ok(inFlightSnapshot)
assert.equal(inFlightCounter.consume({ systemAccountId: 'in-flight-user', settings: coordinatedSettings, nowMs: firstMinute }).allowed, true)
inFlightCounter.applySyncResults([{
  entryKey: inFlightSnapshot.entryKey,
  sentLocalCount: inFlightSnapshot.localCount,
  remoteTotal: inFlightSnapshot.localCount
}])
assert.equal(inFlightCounter.dirtySnapshot(1)[0]?.localCount, 2)

const boundedCounter = new UserRequestLimitCounter({ maxEntries: 1, cleanupStride: 1, cleanupBatchSize: 1 })
assert.equal(boundedCounter.consume({ systemAccountId: 'bounded-a', settings, nowMs: firstMinute }).allowed, true)
assert.equal(boundedCounter.consume({ systemAccountId: 'bounded-b', settings, nowMs: firstMinute }).allowed, true)
assert.equal(boundedCounter.size(), 1)
assert.equal(boundedCounter.stats().capacityEvictions, 1)
assert.equal(boundedCounter.dirtySnapshot(1)[0]?.systemAccountId, 'bounded-b')

const fairCounter = new UserRequestLimitCounter()
for (const systemAccountId of ['fair-a', 'fair-b', 'fair-c']) {
  fairCounter.consume({ systemAccountId, settings, nowMs: firstMinute })
}
assert.deepEqual(
  fairCounter.dirtySnapshot(2).map((entry) => entry.systemAccountId),
  ['fair-a', 'fair-b']
)
assert.deepEqual(
  fairCounter.dirtySnapshot(2).map((entry) => entry.systemAccountId),
  ['fair-c', 'fair-a']
)

const retentionCounter = new UserRequestLimitCounter()
retentionCounter.consume({ systemAccountId: 'retention-week', settings: weeklySettings, nowMs: firstMinute })
retentionCounter.consume({ systemAccountId: 'retention-month', settings: monthlySettings, nowMs: firstMinute })
const retentionEntries = retentionCounter.dirtySnapshot()
assert.equal(retentionEntries.find((entry) => entry.window === 'perWeek')?.redisTtlMs, 9 * 24 * 60 * 60 * 1_000)
assert.equal(retentionEntries.find((entry) => entry.window === 'perMonth')?.redisTtlMs, 35 * 24 * 60 * 60 * 1_000)

console.log('user request limit regression passed')

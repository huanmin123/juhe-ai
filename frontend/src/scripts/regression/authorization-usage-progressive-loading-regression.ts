import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { buildAuthorizationUsageSignature, createAuthorizationUsageRequestGate } from '../../views/authorizations/authorizationUsageRequestGate'

const frontendRoot = resolve(import.meta.dirname, '..', '..')
const teamView = readFileSync(resolve(frontendRoot, 'views', 'authorizations', 'AuthorizationTeamUsageView.vue'), 'utf8')
const userView = readFileSync(resolve(frontendRoot, 'views', 'authorizations', 'AuthorizationUserUsageView.vue'), 'utf8')
const dateRange = readFileSync(resolve(frontendRoot, 'views', 'authorizations', 'useAuthorizationUsageDateRange.ts'), 'utf8')
const requestGate = readFileSync(resolve(frontendRoot, 'views', 'authorizations', 'authorizationUsageRequestGate.ts'), 'utf8')

for (const [name, source] of [['team', teamView], ['user', userView]] as const) {
  assert.doesNotMatch(source, /includeSummary/, `${name} rows request must use the strict rows contract`)
  assert.match(source, /async function loadUsageSummary\(\)/, `${name} summary must have an independent loader`)
  assert.doesNotMatch(source, /async function loadOptions(?:Now)?\(/, `${name} dead eager options loader must be removed`)
  assert.match(source, /function reloadFromFirstPage[\s\S]*loadUsageSummary\(\)[\s\S]*loadData\(/, `${name} filter changes must reload rows and summary`)
  assert.match(source, /summaryError/, `${name} summary failure must have an independent visible state`)
  assert.match(source, /rowsError/, `${name} rows failure must have an independent visible state`)
  assert.match(source, /watch\(\(\) => authState\.revision\.value/, `${name} must reload when the authentication revision changes`)
  assert.match(source, /onDeactivated\([\s\S]*requestGate\.deactivate\(\)/, `${name} must invalidate requests when KeepAlive deactivates`)
  assert.match(source, /requestSignature:\s*\(\)\s*=>\s*\[currentUsageSignature\(\), requestEpoch\.value\]/, `${name} KeepAlive activation must bypass a superseded in-flight rows promise`)
  assert.match(source, /requestGate\.beginBatch\(currentUsageSignature\(\)\)/, `${name} combined refresh must reset the shared range comparison batch`)
}

assert.match(teamView, /teamUsageSummary\(params\)/, 'team summary must use its dedicated endpoint')
assert.match(userView, /userUsageSummary\(params\)/, 'user summary must use its dedicated endpoint')
assert.match(dateRange, /loadUsageStatsWindow\(\{ viewScope: options\.viewScope \?\? 'self' \}\)/, 'authorization date range must request the matching admin/self window')
assert.match(requestGate, /input\.authRevision/, 'business signature must include auth revision')
assert.match(requestGate, /input\.viewerId/, 'business signature must include viewer identity')
assert.match(requestGate, /input\.ownerSystemAccountId/, 'business signature must include effective owner scope')

const signatureA = buildAuthorizationUsageSignature({ kind: 'team', scope: 'admin', authRevision: 1, viewerId: 'admin', viewerRole: 'admin', ownerSystemAccountId: 'owner-a' })
const signatureB = buildAuthorizationUsageSignature({ kind: 'team', scope: 'admin', authRevision: 1, viewerId: 'admin', viewerRole: 'admin', ownerSystemAccountId: 'owner-b' })
const signatureANextAuth = buildAuthorizationUsageSignature({ kind: 'team', scope: 'admin', authRevision: 2, viewerId: 'admin', viewerRole: 'admin', ownerSystemAccountId: 'owner-a' })
const gate = createAuthorizationUsageRequestGate()
const staleA = gate.begin('summary', signatureA)
const currentB = gate.begin('summary', signatureB)
assert.equal(gate.isCurrent(currentB, signatureB), true, 'B request must be current after A→B')
assert.equal(gate.isCurrent(staleA, signatureB), false, 'late A response must not overwrite B')
const currentA = gate.begin('summary', signatureA)
assert.equal(gate.isCurrent(staleA, signatureA), false, 'A→B→A must reject the first A generation')
assert.equal(gate.isCurrent(currentA, signatureA), true, 'A→B→A must accept only the newest A generation')
assert.equal(gate.acceptRange(currentA, signatureA, { startDate: '2026-01-01', endDate: '2026-01-01', days: 1, maxDays: 31 }), true)
const rowsA = gate.begin('rows', signatureA)
assert.equal(gate.acceptRange(rowsA, signatureA, { startDate: '2026-01-02', endDate: '2026-01-02', days: 1, maxDays: 31 }), false, 'rows and summary must reject mismatched server ranges')
gate.beginBatch(signatureA)
const refreshedSummaryA = gate.begin('summary', signatureA)
const refreshedRowsA = gate.begin('rows', signatureA)
const nextRange = { startDate: '2026-01-02', endDate: '2026-01-02', days: 1, maxDays: 31 }
assert.equal(gate.acceptRange(refreshedSummaryA, signatureA, nextRange), true, 'same-signature explicit refresh must accept a newly advanced range')
assert.equal(gate.acceptRange(refreshedRowsA, signatureA, nextRange), true, 'rows and summary in the new batch must agree on the advanced range')
assert.notEqual(signatureA, signatureANextAuth, 'auth revision changes must change the business signature')
gate.deactivate()
assert.equal(gate.isCurrent(currentA, signatureA), false, 'deactivation must invalidate in-flight responses')

console.log('authorization usage progressive loading regression passed')

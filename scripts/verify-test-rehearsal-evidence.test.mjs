import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdir, mkdtemp, rm, symlink, writeFile } from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'

import { verifyEvidenceManifest } from './verify-test-rehearsal-evidence.mjs'

function sha256(value) {
  return createHash('sha256').update(value).digest('hex')
}

function hashStringList(values) {
  return sha256([...values].sort().map((value) => `${value}\n`).join(''))
}

function stableJson(value) {
  if (value === null || typeof value !== 'object') return JSON.stringify(value)
  if (Array.isArray(value)) return `[${value.map(stableJson).join(',')}]`
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJson(value[key])}`).join(',')}}`
}

const root = await mkdtemp(path.join(os.tmpdir(), 'juhe-ai-evidence-'))
try {
  const files = [
    { path: 'release/digests.json', contents: Buffer.from('{"ok":true}\n') },
    { path: 'smoke/canary.json', contents: Buffer.from('{"request":"passed"}\n') },
    { path: 'accounts/credentials-policy.json', contents: Buffer.from('{"policy":"test-only-equivalent"}\n') }
  ]
  const closure = {
    schemaVersion: 1,
    approvedCanaryAccountIds: ['canary-1'],
    sourceAccountIds: ['source-1'],
    selectedAccountIds: ['canary-1', 'source-1'],
    selectedAccountIdsHash: hashStringList(['canary-1', 'source-1']),
    systemAccountIds: ['system-1'],
    systemTeamIds: ['team-1'],
    groupIds: ['group-1'],
    routeStrategyIds: ['route-1'],
    resourceAuthorizationIds: ['authorization-1'],
    resourceAuthorizationGrantIds: ['grant-1'],
    apiKeyIds: ['key-source-1'],
    apiKeyRemap: [{ sourceId: 'key-source-1', targetId: 'key-target-1' }],
    accountSystemAccountLinks: [
      { accountId: 'canary-1', systemAccountId: 'system-1' },
      { accountId: 'source-1', systemAccountId: 'system-1' }
    ],
    accountGroupLinks: [{ accountId: 'canary-1', groupId: 'group-1' }],
    accountRouteStrategyLinks: [{ accountId: 'canary-1', routeStrategyId: 'route-1' }],
    authorizationLinks: [{
      id: 'authorization-1',
      resourceType: 'account',
      resourceId: 'source-1',
      resourceOwnerSystemAccountId: 'system-1',
      granteeSystemAccountId: 'system-1'
    }]
  }
  closure.approvedCanaryAccountIdsHash = hashStringList(closure.approvedCanaryAccountIds)
  closure.sourceAccountIdsHash = hashStringList(closure.sourceAccountIds)
  closure.systemAccountIdsHash = hashStringList(closure.systemAccountIds)
  closure.systemTeamIdsHash = hashStringList(closure.systemTeamIds)
  closure.groupIdsHash = hashStringList(closure.groupIds)
  closure.routeStrategyIdsHash = hashStringList(closure.routeStrategyIds)
  closure.resourceAuthorizationIdsHash = hashStringList(closure.resourceAuthorizationIds)
  closure.resourceAuthorizationGrantIdsHash = hashStringList(closure.resourceAuthorizationGrantIds)
  closure.apiKeyIdsHash = hashStringList(closure.apiKeyIds)
  closure.apiKeyRemapHash = sha256(stableJson(closure.apiKeyRemap))
  const closureContents = Buffer.from(`${JSON.stringify(closure)}\n`)
  files.push({ path: 'accounts/closure.json', contents: closureContents })
  for (const item of files) {
    const filePath = path.join(root, item.path)
    await mkdir(path.dirname(filePath), { recursive: true })
    await writeFile(filePath, item.contents, { flag: 'wx' })
  }
  const manifest = {
    schemaVersion: 1,
    // Deliberately unsorted: the verifier must canonicalize path order itself.
    files: files.map((item) => ({ path: item.path, sha256: sha256(item.contents) })).reverse()
  }
  const manifestDigest = sha256(stableJson({
    schemaVersion: 1,
    files: [...manifest.files].sort((left, right) => left.path < right.path ? -1 : left.path > right.path ? 1 : 0)
  }))
  await writeFile(path.join(root, 'manifest.json'), JSON.stringify(manifest, null, 2), { flag: 'wx' })

  const evidence = {
    release: { evidenceRefs: ['release/digests.json'] },
    smoke: { evidenceRefs: ['smoke/canary.json'] },
    accounts: {
      credentialsEvidenceRef: 'accounts/credentials-policy.json',
      closureEvidenceRef: 'accounts/closure.json',
      approvedCanaryAccountIdsHash: closure.approvedCanaryAccountIdsHash,
      sourceAccountIdsHash: closure.sourceAccountIdsHash,
      systemAccountIdsHash: closure.systemAccountIdsHash,
      systemTeamIdsHash: closure.systemTeamIdsHash,
      groupIdsHash: closure.groupIdsHash,
      routeStrategyIdsHash: closure.routeStrategyIdsHash,
      resourceAuthorizationIdsHash: closure.resourceAuthorizationIdsHash,
      resourceAuthorizationGrantIdsHash: closure.resourceAuthorizationGrantIdsHash,
      apiKeyIdsHash: closure.apiKeyIdsHash,
      apiKeyRemapHash: closure.apiKeyRemapHash,
      approvedCanaryCount: 1
    },
    controls: { evidenceManifestDigest: manifestDigest }
  }
  assert.deepEqual(await verifyEvidenceManifest(evidence, root), {
    status: 'passed',
    evidenceRefs: 4,
    evidenceManifestDigest: manifestDigest
  })
  const duplicateLinkEvidence = structuredClone(evidence)
  const duplicateLinkClosure = structuredClone(closure)
  duplicateLinkClosure.accountGroupLinks.push({ accountId: 'canary-1', groupId: 'group-1' })
  const duplicateLinkContents = Buffer.from(`${JSON.stringify(duplicateLinkClosure)}\n`)
  await writeFile(path.join(root, 'accounts/closure.json'), duplicateLinkContents)
  const duplicateLinkManifest = {
    schemaVersion: 1,
    files: manifest.files.map((item) => item.path === 'accounts/closure.json'
      ? { path: 'accounts/closure.json', sha256: sha256(duplicateLinkContents) }
      : item)
  }
  const duplicateLinkManifestDigest = sha256(stableJson({
    schemaVersion: 1,
    files: [...duplicateLinkManifest.files].sort((left, right) => left.path < right.path ? -1 : left.path > right.path ? 1 : 0)
  }))
  await writeFile(path.join(root, 'manifest.json'), JSON.stringify(duplicateLinkManifest, null, 2))
  duplicateLinkEvidence.controls.evidenceManifestDigest = duplicateLinkManifestDigest
  await assert.rejects(
    () => verifyEvidenceManifest(duplicateLinkEvidence, root),
    /accountGroupLinks.*重复关联记录/u
  )
  await writeFile(path.join(root, 'manifest.json'), JSON.stringify(manifest, null, 2))
  await writeFile(path.join(root, 'accounts/closure.json'), closureContents)
  const mismatchedClosureHashEvidence = structuredClone(evidence)
  mismatchedClosureHashEvidence.accounts.systemAccountIdsHash = '0'.repeat(64)
  await assert.rejects(
    () => verifyEvidenceManifest(mismatchedClosureHashEvidence, root),
    /systemAccountIdsHash.*账户闭包/u
  )

  await writeFile(path.join(root, 'unregistered.json'), '{"unexpected":true}\n', { flag: 'wx' })
  await assert.rejects(() => verifyEvidenceManifest(evidence, root), /未登记文件/u)
  await rm(path.join(root, 'unregistered.json'))

  await writeFile(path.join(root, 'smoke/canary.json'), '{"request":"changed"}\n')
  await assert.rejects(() => verifyEvidenceManifest(evidence, root), /证据文件摘要不匹配/u)
  await assert.rejects(() => verifyEvidenceManifest({ ...evidence, release: { evidenceRefs: ['../outside.json'] } }, root), /manifest 文件列表/u)

  await symlink(path.join(root, 'release/digests.json'), path.join(root, 'release-link.json'))
  const linkedManifest = {
    schemaVersion: 1,
    files: [
      { path: 'release-link.json', sha256: sha256(files[0].contents) },
      { path: 'smoke/canary.json', sha256: sha256(Buffer.from('{"request":"changed"}\n')) }
    ]
  }
  const linkedManifestDigest = sha256(stableJson({
    schemaVersion: 1,
    files: [...linkedManifest.files].sort((left, right) => left.path < right.path ? -1 : left.path > right.path ? 1 : 0)
  }))
  await rm(path.join(root, 'manifest.json'))
  await writeFile(path.join(root, 'manifest.json'), JSON.stringify(linkedManifest, null, 2), { flag: 'wx' })
  const linkedEvidence = {
    release: { evidenceRefs: ['release-link.json'] },
    smoke: { evidenceRefs: ['smoke/canary.json'] },
    controls: { evidenceManifestDigest: linkedManifestDigest }
  }
  await assert.rejects(() => verifyEvidenceManifest(linkedEvidence, root), /符号链接/u)
} finally {
  await rm(root, { recursive: true, force: true })
}

console.log('test rehearsal evidence binding passed')

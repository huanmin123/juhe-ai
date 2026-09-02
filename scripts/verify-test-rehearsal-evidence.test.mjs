import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdir, mkdtemp, rm, symlink, writeFile } from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'

import { verifyEvidenceManifest } from './verify-test-rehearsal-evidence.mjs'

function sha256(value) {
  return createHash('sha256').update(value).digest('hex')
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
    accounts: { credentialsEvidenceRef: 'accounts/credentials-policy.json' },
    controls: { evidenceManifestDigest: manifestDigest }
  }
  assert.deepEqual(await verifyEvidenceManifest(evidence, root), {
    status: 'passed',
    evidenceRefs: 3,
    evidenceManifestDigest: manifestDigest
  })

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

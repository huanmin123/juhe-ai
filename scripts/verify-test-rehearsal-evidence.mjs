#!/usr/bin/env node

import { createHash } from 'node:crypto'
import { lstat, readdir, readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  EVIDENCE_REF_PATTERN,
  validateTestRehearsalEvidence
} from './validate-test-rehearsal.mjs'

const SHA256_HEX_PATTERN = /^[0-9a-f]{64}$/u
const MANIFEST_SCHEMA_VERSION = 1
const MAX_EVIDENCE_FILE_BYTES = 16 * 1024 * 1024
const ACCOUNT_CLOSURE_SCHEMA_VERSION = 1
const ACCOUNT_CLOSURE_ID_FIELDS = Object.freeze([
  'approvedCanaryAccountIds',
  'sourceAccountIds',
  'selectedAccountIds',
  'systemAccountIds',
  'systemTeamIds',
  'groupIds',
  'routeStrategyIds',
  'resourceAuthorizationIds',
  'resourceAuthorizationGrantIds',
  'apiKeyIds'
])

function isRecord(value) {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function stableJson(value) {
  if (value === null || typeof value !== 'object') return JSON.stringify(value)
  if (Array.isArray(value)) return `[${value.map(stableJson).join(',')}]`
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJson(value[key])}`).join(',')}}`
}

function sha256(value) {
  return createHash('sha256').update(value).digest('hex')
}

function compareEvidencePath(left, right) {
  // Evidence manifests can be generated on Windows and verified on Linux.
  // Locale-sensitive comparison would make the same file set hash differently.
  if (left < right) return -1
  if (left > right) return 1
  return 0
}

async function sha256File(filePath) {
  const fileStat = await lstat(filePath)
  if (!fileStat.isFile() || fileStat.isSymbolicLink()) {
    throw new Error(`证据引用必须是受控目录内的非链接普通文件：${filePath}`)
  }
  if (fileStat.size > MAX_EVIDENCE_FILE_BYTES) {
    throw new Error(`证据文件超过 ${MAX_EVIDENCE_FILE_BYTES} 字节限制：${filePath}`)
  }
  return sha256(await readFile(filePath))
}

async function listEvidenceFiles(root, current = '') {
  const directory = path.join(root, current)
  const entries = await readdir(directory, { withFileTypes: true })
  const files = []
  for (const entry of entries) {
    const relative = current ? `${current}/${entry.name}` : entry.name
    if (!EVIDENCE_REF_PATTERN.test(relative)) {
      throw new Error(`受控证据目录包含非法路径：${relative}`)
    }
    const absolute = path.join(root, ...relative.split('/'))
    const entryStat = await lstat(absolute)
    if (entryStat.isSymbolicLink()) {
      throw new Error(`受控证据目录不得包含符号链接：${relative}`)
    }
    if (entryStat.isDirectory()) {
      files.push(...await listEvidenceFiles(root, relative))
      continue
    }
    if (!entryStat.isFile()) {
      throw new Error(`受控证据目录只能包含普通文件或目录：${relative}`)
    }
    files.push(relative)
  }
  return files.sort(compareEvidencePath)
}

function collectEvidenceRefs(value, refs = new Set()) {
  if (Array.isArray(value)) {
    for (const item of value) collectEvidenceRefs(item, refs)
    return refs
  }
  if (!isRecord(value)) return refs
  for (const [key, child] of Object.entries(value)) {
    const isEvidenceReferenceKey = key === 'evidenceRef' || key.endsWith('EvidenceRef')
    if (key === 'evidenceRefs' && Array.isArray(child)) {
      for (const ref of child) if (typeof ref === 'string') refs.add(ref)
      continue
    }
    if (isEvidenceReferenceKey && typeof child === 'string') {
      refs.add(child)
      continue
    }
    collectEvidenceRefs(child, refs)
  }
  return refs
}

function resolveEvidencePath(evidenceRoot, reference) {
  if (!EVIDENCE_REF_PATTERN.test(reference)) {
    throw new Error(`证据引用路径非法：${reference}`)
  }
  const root = path.resolve(evidenceRoot)
  const resolved = path.resolve(root, reference)
  const relative = path.relative(root, resolved)
  if (!relative || relative.startsWith(`..${path.sep}`) || relative === '..' || path.isAbsolute(relative)) {
    throw new Error(`证据引用越出受控目录：${reference}`)
  }
  if (path.normalize(relative) === 'manifest.json') {
    throw new Error('manifest.json 不得作为自身证据引用')
  }
  return resolved
}

function hashStringList(values) {
  const hash = createHash('sha256')
  for (const value of [...values].sort(compareEvidencePath)) hash.update(value).update('\n')
  return hash.digest('hex')
}

function normalizeIdList(value, label, { allowEmpty = true } = {}) {
  if (!Array.isArray(value) || (!allowEmpty && value.length === 0)) {
    throw new Error(`账户闭包 ${label} 必须是${allowEmpty ? '' : '非空'}字符串数组`)
  }
  const ids = value.map((item) => {
    if (typeof item !== 'string' || item.trim() === '') throw new Error(`账户闭包 ${label} 含空 ID`)
    return item
  })
  if (new Set(ids).size !== ids.length) throw new Error(`账户闭包 ${label} 不得包含重复 ID`)
  return ids
}

function normalizeLinks(value, label, fields) {
  if (!Array.isArray(value)) throw new Error(`账户闭包 ${label} 必须是数组`)
  const seen = new Set()
  return value.map((item, index) => {
    if (!isRecord(item)) throw new Error(`账户闭包 ${label}[${index}] 必须是对象`)
    const normalized = {}
    for (const field of fields) {
      if (typeof item[field] !== 'string' || item[field].trim() === '') {
        throw new Error(`账户闭包 ${label}[${index}].${field} 必须是非空字符串`)
      }
      normalized[field] = item[field]
    }
    const key = stableJson(normalized)
    if (seen.has(key)) throw new Error(`账户闭包 ${label} 不得包含重复关联记录`)
    seen.add(key)
    return normalized
  })
}

function normalizeApiKeyRemap(value) {
  const remap = normalizeLinks(value, 'apiKeyRemap', ['sourceId', 'targetId'])
    .sort((left, right) => compareEvidencePath(left.sourceId, right.sourceId) || compareEvidencePath(left.targetId, right.targetId))
  if (new Set(remap.map((item) => item.sourceId)).size !== remap.length) {
    throw new Error('账户闭包 apiKeyRemap 的 sourceId 不得重复')
  }
  if (new Set(remap.map((item) => item.targetId)).size !== remap.length) {
    throw new Error('账户闭包 apiKeyRemap 的 targetId 不得重复')
  }
  return remap
}

function apiKeyRemapHash(remap) {
  return sha256(stableJson(remap))
}

async function verifyAccountClosureEvidence(evidence, evidenceRoot) {
  const accounts = evidence?.accounts
  if (!isRecord(accounts) || typeof accounts.closureEvidenceRef !== 'string') return

  const closurePath = resolveEvidencePath(evidenceRoot, accounts.closureEvidenceRef)
  let closure
  try {
    closure = JSON.parse(await readFile(closurePath, 'utf8'))
  } catch (error) {
    throw new Error(`账户闭包证据无法读取或不是合法 JSON：${accounts.closureEvidenceRef}`)
  }
  if (!isRecord(closure) || closure.schemaVersion !== ACCOUNT_CLOSURE_SCHEMA_VERSION) {
    throw new Error('账户闭包证据必须是 schemaVersion=1 的对象')
  }

  const lists = {}
  for (const field of ACCOUNT_CLOSURE_ID_FIELDS) {
    lists[field] = normalizeIdList(closure[field], field, { allowEmpty: field !== 'approvedCanaryAccountIds' })
  }
  if (!Number.isInteger(accounts.approvedCanaryCount)
    || accounts.approvedCanaryCount !== lists.approvedCanaryAccountIds.length) {
    throw new Error('账户闭包 approvedCanaryCount 与 approvedCanaryAccountIds 不一致')
  }
  const selected = new Set(lists.selectedAccountIds)
  const expectedSelected = new Set([...lists.approvedCanaryAccountIds, ...lists.sourceAccountIds])
  if (selected.size !== expectedSelected.size || [...selected].some((id) => !expectedSelected.has(id))) {
    throw new Error('账户闭包 selectedAccountIds 必须恰好是 canary 与 source 账户并集')
  }
  if (closure.selectedAccountIdsHash !== hashStringList(lists.selectedAccountIds)) {
    throw new Error('账户闭包 selectedAccountIdsHash 与 selectedAccountIds 不一致')
  }

  const evidenceHashFields = [
    ['approvedCanaryAccountIdsHash', 'approvedCanaryAccountIds'],
    ['sourceAccountIdsHash', 'sourceAccountIds'],
    ['systemAccountIdsHash', 'systemAccountIds'],
    ['systemTeamIdsHash', 'systemTeamIds'],
    ['groupIdsHash', 'groupIds'],
    ['routeStrategyIdsHash', 'routeStrategyIds'],
    ['resourceAuthorizationIdsHash', 'resourceAuthorizationIds'],
    ['resourceAuthorizationGrantIdsHash', 'resourceAuthorizationGrantIds'],
    ['apiKeyIdsHash', 'apiKeyIds']
  ]
  for (const [evidenceField, listField] of evidenceHashFields) {
    if (accounts[evidenceField] !== hashStringList(lists[listField])) {
      throw new Error(`accounts.${evidenceField} 与账户闭包 ${listField} 不一致`)
    }
  }

  const apiKeyRemap = normalizeApiKeyRemap(closure.apiKeyRemap)
  if (accounts.apiKeyRemapHash !== apiKeyRemapHash(apiKeyRemap)) {
    throw new Error('accounts.apiKeyRemapHash 与账户闭包 apiKeyRemap 不一致')
  }
  const selectedSet = new Set(lists.selectedAccountIds)
  const systemSet = new Set(lists.systemAccountIds)
  const groupSet = new Set(lists.groupIds)
  const routeSet = new Set(lists.routeStrategyIds)
  const authorizationSet = new Set(lists.resourceAuthorizationIds)
  for (const link of normalizeLinks(closure.accountSystemAccountLinks, 'accountSystemAccountLinks', ['accountId', 'systemAccountId'])) {
    if (!selectedSet.has(link.accountId) || !systemSet.has(link.systemAccountId)) {
      throw new Error('账户闭包 accountSystemAccountLinks 存在越界引用')
    }
  }
  for (const link of normalizeLinks(closure.accountGroupLinks, 'accountGroupLinks', ['accountId', 'groupId'])) {
    if (!selectedSet.has(link.accountId) || !groupSet.has(link.groupId)) {
      throw new Error('账户闭包 accountGroupLinks 存在越界引用')
    }
  }
  for (const link of normalizeLinks(closure.accountRouteStrategyLinks, 'accountRouteStrategyLinks', ['accountId', 'routeStrategyId'])) {
    if (!selectedSet.has(link.accountId) || !routeSet.has(link.routeStrategyId)) {
      throw new Error('账户闭包 accountRouteStrategyLinks 存在越界引用')
    }
  }
  for (const link of normalizeLinks(closure.authorizationLinks, 'authorizationLinks', [
    'id', 'resourceType', 'resourceId', 'resourceOwnerSystemAccountId', 'granteeSystemAccountId'
  ])) {
    if (!authorizationSet.has(link.id)
      || !systemSet.has(link.resourceOwnerSystemAccountId)
      || !systemSet.has(link.granteeSystemAccountId)
      || !['account', 'group'].includes(link.resourceType)
      || (link.resourceType === 'account' && !selectedSet.has(link.resourceId))
      || (link.resourceType === 'group' && !groupSet.has(link.resourceId))) {
      throw new Error('账户闭包 authorizationLinks 存在越界或类型引用')
    }
  }
}

function normalizeManifest(manifest) {
  if (!isRecord(manifest) || manifest.schemaVersion !== MANIFEST_SCHEMA_VERSION || !Array.isArray(manifest.files)) {
    throw new Error('证据 manifest 必须是 { schemaVersion: 1, files: [...] }')
  }
  const files = manifest.files.map((item, index) => {
    if (!isRecord(item) || typeof item.path !== 'string' || !EVIDENCE_REF_PATTERN.test(item.path) || !SHA256_HEX_PATTERN.test(item.sha256)) {
      throw new Error(`证据 manifest files[${index}] 必须包含合法 path/sha256`)
    }
    return { path: item.path, sha256: item.sha256 }
  }).sort((left, right) => compareEvidencePath(left.path, right.path))
  if (new Set(files.map((item) => item.path)).size !== files.length) {
    throw new Error('证据 manifest 不得包含重复 path')
  }
  return { schemaVersion: MANIFEST_SCHEMA_VERSION, files }
}

export async function verifyEvidenceManifest(evidence, evidenceRoot) {
  if (!isRecord(evidence) || !isRecord(evidence.controls) || typeof evidence.controls.evidenceManifestDigest !== 'string') {
    throw new Error('缺少 controls.evidenceManifestDigest')
  }
  const root = path.resolve(evidenceRoot)
  const manifestPath = path.join(root, 'manifest.json')
  const manifestStat = await lstat(manifestPath)
  if (!manifestStat.isFile() || manifestStat.isSymbolicLink()) {
    throw new Error('manifest.json 必须是受控目录内的非链接普通文件')
  }
  const manifest = normalizeManifest(JSON.parse(await readFile(manifestPath, 'utf8')))
  const expectedDigest = sha256(stableJson(manifest))
  if (evidence.controls.evidenceManifestDigest !== expectedDigest) {
    throw new Error(`evidenceManifestDigest 不匹配：expected=${expectedDigest}`)
  }

  const expectedRefs = [...collectEvidenceRefs(evidence)].sort(compareEvidencePath)
  const manifestRefs = manifest.files.map((item) => item.path)
  if (stableJson(expectedRefs) !== stableJson(manifestRefs)) {
    throw new Error('manifest 文件列表必须与 evidence 中全部 evidenceRef 完全一致')
  }

  const actualFiles = (await listEvidenceFiles(root)).filter((file) => file !== 'manifest.json')
  if (stableJson(actualFiles) !== stableJson(manifestRefs)) {
    throw new Error('受控证据目录存在未登记文件，或 manifest 遗漏了已登记证据文件')
  }

  for (const item of manifest.files) {
    const actualDigest = await sha256File(resolveEvidencePath(root, item.path))
    if (actualDigest !== item.sha256) {
      throw new Error(`证据文件摘要不匹配：${item.path}`)
    }
  }
  await verifyAccountClosureEvidence(evidence, root)
  return { status: 'passed', evidenceRefs: manifest.files.length, evidenceManifestDigest: expectedDigest }
}

async function main() {
  const args = process.argv.slice(2)
  if (args.length !== 2 || args.some((arg) => arg.startsWith('--'))) {
    throw new Error('用法：node scripts/verify-test-rehearsal-evidence.mjs <test-rehearsal-evidence.json> <evidence-root>')
  }
  const evidence = JSON.parse(await readFile(path.resolve(args[0]), 'utf8'))
  const validation = validateTestRehearsalEvidence(evidence)
  if (validation.status !== 'passed') {
    process.stdout.write(`${JSON.stringify(validation, null, 2)}\n`)
    process.exitCode = 3
    return
  }
  try {
    process.stdout.write(`${JSON.stringify(await verifyEvidenceManifest(evidence, args[1]), null, 2)}\n`)
  } catch (error) {
    process.stdout.write(`${JSON.stringify({ status: 'blocked', blockers: [error instanceof Error ? error.message : String(error)] }, null, 2)}\n`)
    process.exitCode = 3
  }
}

const isDirectRun = process.argv[1]
  && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url))

if (isDirectRun) {
  main().catch((error) => {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`)
    process.exitCode = 2
  })
}

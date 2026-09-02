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

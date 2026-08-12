#!/usr/bin/env node

import { lstat, readFile, readdir } from 'node:fs/promises'
import { realpathSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { parse } from 'acorn'

import {
  FRONTEND_API_BASE_ROOT,
  FrontendApiBaseValidationError,
  validateFrontendApiBase
} from './frontend-api-base-contract.mjs'

const FORBIDDEN_PATH_SEGMENTS = new Set(['data', 'logs', 'node_modules'])
const FORBIDDEN_PERSISTENCE_SUFFIXES = [
  '.sqlite',
  '.sqlite3',
  '.db',
  '.db3',
  '.dump',
  '.rdb',
  '.aof'
]
const FRONTEND_API_MARKER = FRONTEND_API_BASE_ROOT
const FRONTEND_TEXT_EXTENSIONS = new Set([
  '.css',
  '.html',
  '.js',
  '.json',
  '.map',
  '.mjs',
  '.svg',
  '.txt',
  '.webmanifest',
  '.xml'
])

export class ReleasePackageValidationError extends Error {
  constructor(message) {
    super(message)
    this.name = 'ReleasePackageValidationError'
  }
}

function fail(relativePath, reason) {
  const displayPath = relativePath || '.'
  throw new ReleasePackageValidationError(
    `Release package validation failed at "${displayPath}": ${reason}`
  )
}

function validateReleasePath(relativePath) {
  if (!relativePath) {
    return
  }

  const pathSegments = relativePath.split('/').filter(Boolean)
  const lowerSegments = pathSegments.map((segment) => segment.toLowerCase())
  const forbiddenSegment = lowerSegments.find((segment) => FORBIDDEN_PATH_SEGMENTS.has(segment))

  if (forbiddenSegment) {
    fail(relativePath, `forbidden path segment "${forbiddenSegment}"`)
  }

  const basename = lowerSegments.at(-1) ?? ''

  if (basename.startsWith('.env') && basename !== '.env.example') {
    fail(relativePath, 'real or environment-specific .env files are forbidden')
  }

  if (basename.endsWith('.log')) {
    fail(relativePath, 'log files are forbidden')
  }

  if (FORBIDDEN_PERSISTENCE_SUFFIXES.some((suffix) => basename.endsWith(suffix))) {
    fail(relativePath, 'database or persistence files are forbidden')
  }

  if (/(?:-wal|-shm|-journal)$/u.test(basename)) {
    fail(relativePath, 'database journal files are forbidden')
  }

  if (/^appendonly\.aof(?:\.|$)/u.test(basename)) {
    fail(relativePath, 'Redis append-only persistence files are forbidden')
  }
}

function isEscapedCharacter(line, index) {
  let backslashCount = 0
  for (let cursor = index - 1; cursor >= 0 && line[cursor] === '\\'; cursor -= 1) {
    backslashCount += 1
  }
  return backslashCount % 2 === 1
}

export function scanJavaScriptLexicalRanges(contents) {
  const ranges = []
  const comments = []
  const syntaxTree = parse(contents, {
    allowAwaitOutsideFunction: true,
    allowHashBang: true,
    ecmaVersion: 'latest',
    onComment: comments,
    sourceType: 'module'
  })
  for (const comment of comments) {
    ranges.push({ type: 'comment', start: comment.start, end: comment.end })
  }

  const visited = new WeakSet()
  function visit(value) {
    if (!value || typeof value !== 'object' || visited.has(value)) {
      return
    }
    visited.add(value)

    if (value.type === 'Literal' && typeof value.value === 'string') {
      ranges.push({ type: 'string', start: value.start + 1, end: value.end - 1 })
    } else if (value.type === 'TemplateLiteral') {
      const rangeType = value.expressions.length === 0 ? 'string' : 'dynamic-string'
      for (const quasi of value.quasis) {
        ranges.push({ type: rangeType, start: quasi.start, end: quasi.end })
      }
    }

    for (const child of Object.values(value)) {
      if (Array.isArray(child)) {
        for (const item of child) {
          visit(item)
        }
      } else {
        visit(child)
      }
    }
  }
  visit(syntaxTree)
  return ranges.sort((left, right) => left.start - right.start)
}

function extractQuotedCandidate(line, markerOffset, markerLength) {
  for (let quoteStart = markerOffset - 1; quoteStart >= 0; quoteStart -= 1) {
    const quote = line[quoteStart]
    if ((quote !== '"' && quote !== "'" && quote !== '`')
      || isEscapedCharacter(line, quoteStart)) {
      continue
    }

    for (let quoteEnd = markerOffset + markerLength; quoteEnd < line.length; quoteEnd += 1) {
      if (line[quoteEnd] === quote && !isEscapedCharacter(line, quoteEnd)) {
        return line.slice(quoteStart + 1, quoteEnd)
      }
    }
    return null
  }
  return null
}

function isRootTokenStartBoundary(character) {
  return character === undefined || /[\s"'`,;{\[<(=]/u.test(character)
}

function isRootTokenEndBoundary(character) {
  return character === undefined || /[\s"'`<>]/u.test(character)
}

function isStandaloneDocumentationReference(candidate) {
  const rootIndex = candidate.indexOf(FRONTEND_API_BASE_ROOT)
  if (rootIndex <= 0) {
    return false
  }

  const rootEnd = rootIndex + FRONTEND_API_BASE_ROOT.length
  const prefix = candidate.slice(0, rootIndex)
  const suffix = candidate.slice(rootEnd)
  const outsideReference = `${prefix}${suffix}`
  return /\s$/u.test(prefix)
    && isRootTokenEndBoundary(candidate[rootEnd])
    && /[\u3400-\u9fff]/u.test(outsideReference)
    && !/[\\/:=%]/u.test(outsideReference)
}

export function extractFrontendApiCandidate(line, markerOffset, markerLength) {
  const quotedCandidate = extractQuotedCandidate(line, markerOffset, markerLength)
  if (quotedCandidate !== null) {
    return quotedCandidate
  }

  const markerEnd = markerOffset + markerLength
  const beforeMarker = line.slice(0, markerOffset)
  const cssUrlStart = beforeMarker.toLowerCase().lastIndexOf('url(')
  if (cssUrlStart >= 0) {
    const cssUrlEnd = line.indexOf(')', markerEnd)
    if (cssUrlEnd >= 0) {
      return line.slice(cssUrlStart + 4, cssUrlEnd).trim()
    }
  }

  const rootStart = markerOffset > 0 && line[markerOffset - 1] === '/'
    ? markerOffset - 1
    : markerOffset
  const rootEnd = markerEnd
  if (line.slice(rootStart, rootEnd) === FRONTEND_API_BASE_ROOT
    && isRootTokenStartBoundary(line[rootStart - 1])
    && isRootTokenEndBoundary(line[rootEnd])) {
    return FRONTEND_API_BASE_ROOT
  }

  const schemeMatches = [...beforeMarker.matchAll(/[A-Za-z][A-Za-z0-9+.-]*:/gu)]
  const schemeStart = schemeMatches.at(-1)?.index
  let candidateStart
  if (schemeStart !== undefined) {
    candidateStart = schemeStart
  } else {
    const assignmentStart = Math.max(
      beforeMarker.lastIndexOf('='),
      beforeMarker.lastIndexOf('('),
      beforeMarker.lastIndexOf('>')
    )
    candidateStart = assignmentStart >= 0 ? assignmentStart + 1 : 0
    while (candidateStart < markerOffset && /\s/u.test(line[candidateStart])) {
      candidateStart += 1
    }
  }

  let candidateEnd = line.length
  for (let index = markerEnd; index < line.length; index += 1) {
    if (/[\s"'`<>]/u.test(line[index])) {
      candidateEnd = index
      break
    }
  }
  return line.slice(candidateStart, candidateEnd)
}

function isApiEndpointReference(line, markerOffset, candidate) {
  if (candidate === FRONTEND_API_BASE_ROOT || !candidate.startsWith(FRONTEND_API_BASE_ROOT)) {
    return false
  }
  const beforeMarker = line.slice(0, markerOffset)
  return /(?:\bfetch\s*\(\s*|(?:href|src|action)\s*=\s*)["']\/?$/u.test(beforeMarker)
}

function validateFrontendBundleText(relativePath, contents, frontendState) {
  const markerPattern = /__aisys__(?:[\\/]+)api/giu
  const extension = path.extname(relativePath).toLowerCase()
  const isJavaScript = extension === '.js' || extension === '.mjs'
  const isRuntimeAsset = relativePath.startsWith('frontend/dist/assets/')
    && isJavaScript
  let lexicalRanges = []
  if (isJavaScript) {
    try {
      lexicalRanges = scanJavaScriptLexicalRanges(contents)
    } catch (error) {
      const reason = error instanceof Error ? error.message : String(error)
      fail(relativePath, `frontend JavaScript could not be parsed: ${reason}`)
    }
  }

  for (const marker of contents.matchAll(markerPattern)) {
    const markerEnd = marker.index + marker[0].length
    const lexicalRange = lexicalRanges.find(
      (range) => marker.index >= range.start && markerEnd <= range.end
    )
    if (lexicalRange?.type === 'comment') {
      continue
    }
    if (lexicalRange?.type === 'dynamic-string') {
      fail(
        relativePath,
        `frontend API base must not be assembled by a dynamic template expression (offset ${marker.index})`
      )
    }
    const lineStart = contents.lastIndexOf('\n', marker.index - 1) + 1
    const nextLineBreak = contents.indexOf('\n', markerEnd)
    const lineEnd = nextLineBreak === -1 ? contents.length : nextLineBreak
    const line = contents.slice(lineStart, lineEnd)
    const candidate = lexicalRange?.type === 'string'
      ? contents.slice(lexicalRange.start, lexicalRange.end)
      : extractFrontendApiCandidate(
          line,
          marker.index - lineStart,
          marker[0].length
        )
    if (isStandaloneDocumentationReference(candidate)) {
      continue
    }
    if (isApiEndpointReference(line, marker.index - lineStart, candidate)) {
      continue
    }

    try {
      validateFrontendApiBase(candidate)
      if (isRuntimeAsset && lexicalRange?.type === 'string') {
        frontendState.apiMarkerSeen = true
      }
      continue
    } catch (error) {
      const validationError = error instanceof FrontendApiBaseValidationError ? error : null
      if (validationError?.code === 'windows-drive') {
        fail(relativePath, 'frontend API base contains a Windows drive path')
      }
      if (validationError?.code === 'unc') {
        fail(relativePath, 'frontend API base contains a UNC path')
      }
      if (validationError?.code === 'protocol-relative') {
        fail(relativePath, 'frontend API base must not be protocol-relative')
      }
      if (validationError?.code === 'filesystem') {
        fail(relativePath, 'frontend API base contains a filesystem path')
      }
      fail(relativePath, 'frontend API base contains an invalid absolute URL')
    }
  }

}

async function visitPath(absolutePath, relativePath, linksOnly, frontendState) {
  const stats = await lstat(absolutePath)

  if (stats.isSymbolicLink()) {
    fail(relativePath, 'symbolic links and junctions are forbidden')
  }

  if (!linksOnly) {
    validateReleasePath(relativePath)
  }

  if (stats.isFile()) {
    if (!linksOnly
      && relativePath.startsWith('frontend/dist/')
      && FRONTEND_TEXT_EXTENSIONS.has(path.extname(relativePath).toLowerCase())) {
      const contents = await readFile(absolutePath, 'utf8')
      validateFrontendBundleText(relativePath, contents, frontendState)
    }
    return
  }

  if (!stats.isDirectory()) {
    fail(relativePath, 'only regular files and directories are allowed')
  }

  if (relativePath === 'frontend/dist') {
    frontendState.directorySeen = true
  }

  const entryNames = await readdir(absolutePath)
  entryNames.sort((left, right) => left.localeCompare(right, 'en'))

  for (const entryName of entryNames) {
    const entryAbsolutePath = path.join(absolutePath, entryName)
    const entryRelativePath = relativePath ? `${relativePath}/${entryName}` : entryName
    await visitPath(entryAbsolutePath, entryRelativePath, linksOnly, frontendState)
  }
}

export async function validateReleasePackagePaths(paths, options = {}) {
  if (!Array.isArray(paths) || paths.length === 0) {
    throw new ReleasePackageValidationError('At least one path is required.')
  }

  const linksOnly = options.linksOnly === true

  for (const inputPath of paths) {
    const absolutePath = path.resolve(inputPath)
    const normalizedAbsolutePath = absolutePath.split(path.sep).join('/')
    const initialRelativePath = normalizedAbsolutePath.endsWith('/frontend/dist')
      ? 'frontend/dist'
      : ''
    const frontendState = { apiMarkerSeen: false, directorySeen: false }
    await visitPath(absolutePath, initialRelativePath, linksOnly, frontendState)
    if (!linksOnly && frontendState.directorySeen && !frontendState.apiMarkerSeen) {
      fail('frontend/dist/assets', 'frontend runtime bundle does not contain the required API marker')
    }
  }
}

async function main() {
  const args = process.argv.slice(2)
  let linksOnly = false
  let quiet = false
  const inputPaths = []

  for (const arg of args) {
    if (arg === '--links-only') {
      linksOnly = true
      continue
    }

    if (arg === '--quiet') {
      quiet = true
      continue
    }

    if (arg.startsWith('--')) {
      throw new ReleasePackageValidationError(`Unknown option: ${arg}`)
    }

    inputPaths.push(arg)
  }

  await validateReleasePackagePaths(inputPaths, { linksOnly })

  if (!quiet) {
    const mode = linksOnly ? 'link safety' : 'release content'
    process.stdout.write(`Validated ${mode}: ${inputPaths.length} path(s)\n`)
  }
}

function resolveEntryPath(entryPath) {
  try {
    return realpathSync(entryPath)
  } catch {
    return path.resolve(entryPath)
  }
}

const isDirectRun = process.argv[1]
  && resolveEntryPath(process.argv[1]) === resolveEntryPath(fileURLToPath(import.meta.url))

if (isDirectRun) {
  main().catch((error) => {
    const message = error instanceof Error ? error.message : String(error)
    process.stderr.write(`${message}\n`)
    process.exitCode = 1
  })
}

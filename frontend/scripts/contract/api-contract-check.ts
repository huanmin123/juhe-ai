import assert from 'node:assert/strict'
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

// 前端 API 调用面 ↔ Go gateway 路由注册面 契约比对（静态源码提取）。
//
// 前端侧：解析 frontend/src/api/ 下全部 axios(http.*) 与 fetch 调用点，
//         路径模板中 ${...} 插值归一为参数段 {p}，相对 /__aisys__/api。
// Go 侧：解析 backend-go/projects/gateway 的 kernel Register/RegisterFunc
//         注册面（字面量、prefix 拼接、range 循环、注册闭包、带参 Mount
//         辅助函数、providerPlans 摘要）与 J3b model-checks 独立监听链路
//         (main.go MountScoped → host.go → http.go ServeHTTP switch)。
// 比对：前端集合必须 ⊆ Go 注册面（方法相等 + 参数段位置对齐）；
//         白名单命中差集输出 WARN 不失败；Go 侧多出端点仅信息输出。

const REPO_ROOT = fileURLToPath(new URL('../../..', import.meta.url))
const FRONTEND_API_DIR = join(REPO_ROOT, 'frontend/src/api')
const GO_SCAN_ROOTS = [
  join(REPO_ROOT, 'backend-go/projects/gateway/internal'),
  join(REPO_ROOT, 'backend-go/projects/gateway/cmd/juhe-ai-gateway')
]
const ALLOWLIST_PATH = join(REPO_ROOT, 'frontend/scripts/contract/api-contract-allowlist.json')
const SYSTEM_API_PREFIX = '/__aisys__/api'

// ---------------------------------------------------------------------------
// 通用：文件枚举与字符串感知扫描
// ---------------------------------------------------------------------------

function listFilesRecursive(dir: string, ext: string, exclude: (name: string) => boolean): string[] {
  const out: string[] = []
  if (!existsSync(dir)) return out
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      out.push(...listFilesRecursive(full, ext, exclude))
    } else if (entry.name.endsWith(ext) && !exclude(entry.name)) {
      out.push(full)
    }
  }
  return out
}

const MASK_CODE = 0
const MASK_STR = 1
const MASK_RAW = 2
const MASK_COMMENT = 3

// 字符串感知扫描。quoteStyle: 'ts'（' " ` 字符串，` 为模板整段）或 'go'
// （" 转义字符串、` raw string、' rune）。注释标记为等长掩码以保行号。
function scanSource(text: string, quoteStyle: 'ts' | 'go'): Uint8Array {
  const mask = new Uint8Array(text.length)
  let i = 0
  while (i < text.length) {
    const c = text[i]
    const isLineComment = c === '/' && text[i + 1] === '/'
    const isBlockComment = c === '/' && text[i + 1] === '*'
    if (isLineComment || isBlockComment) {
      const start = i
      if (isLineComment) {
        while (i < text.length && text[i] !== '\n') i++
      } else {
        i += 2
        while (i < text.length && !(text[i] === '*' && text[i + 1] === '/')) i++
        i = Math.min(i + 2, text.length)
      }
      for (let k = start; k < i; k++) {
        if (text[k] !== '\n') mask[k] = MASK_COMMENT
      }
      continue
    }
    const stringQuotes = quoteStyle === 'ts' ? ["'", '"', '`'] : ['"', '`', "'"]
    if (stringQuotes.includes(c)) {
      const quote = c
      const kind = quote === '`' ? MASK_RAW : MASK_STR
      const start = i
      i++
      while (i < text.length) {
        const ch = text[i]
        if (kind === MASK_STR && ch === '\\') {
          i += 2
          continue
        }
        if (ch === quote) {
          i++
          break
        }
        if (kind === MASK_STR && ch === '\n') break // 未闭合字符串按行终止
        i++
      }
      for (let k = start; k < Math.min(i, text.length); k++) mask[k] = kind
      continue
    }
    i++
  }
  return mask
}

function stripComments(text: string, quoteStyle: 'ts' | 'go'): string {
  const mask = scanSource(text, quoteStyle)
  const chars = text.split('')
  for (let k = 0; k < chars.length; k++) {
    if (mask[k] === MASK_COMMENT && chars[k] !== '\n') chars[k] = ' '
  }
  return chars.join('')
}

// 在 mask 感知下做顶层分割（深度为 0 时的分隔符）。offset 为 text 在
// mask 对应源码中的起始偏移；mask 不传则按局部文本扫描。
function splitTopLevel(text: string, separator: string, mask?: Uint8Array, offset = 0): string[] {
  const localMask = mask ?? scanSource(text, 'ts')
  const parts: string[] = []
  let depth = 0
  let start = 0
  for (let i = 0; i < text.length; i++) {
    const m = localMask[i + offset]
    if (m === MASK_STR || m === MASK_RAW || m === MASK_COMMENT) continue
    const c = text[i]
    if (c === '(' || c === '{' || c === '[') depth++
    else if (c === ')' || c === '}' || c === ']') depth--
    else if (c === separator && depth === 0) {
      parts.push(text.slice(start, i))
      start = i + 1
    }
  }
  parts.push(text.slice(start))
  return parts.map((p) => p.trim()).filter((p) => p.length > 0)
}

// 从 openIdx（指向开括号）找到匹配闭括号索引。
function matchBracket(text: string, mask: Uint8Array, openIdx: number): number {
  const open = text[openIdx]
  const close = open === '(' ? ')' : open === '{' ? '}' : open === '[' ? ']' : ''
  if (!close) return -1
  let depth = 0
  for (let i = openIdx; i < text.length; i++) {
    const m = mask[i]
    if (m === MASK_STR || m === MASK_RAW || m === MASK_COMMENT) continue
    if (text[i] === open) depth++
    else if (text[i] === close) {
      depth--
      if (depth === 0) return i
    }
  }
  return -1
}

function lineOfIndex(text: string, index: number): number {
  let line = 1
  for (let i = 0; i < index && i < text.length; i++) {
    if (text[i] === '\n') line++
  }
  return line
}

function rel(path: string): string {
  return relative(REPO_ROOT, path).replaceAll('\\', '/')
}

function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

// 匹配点是否位于函数声明（定义）而非调用。
function isFunctionDeclarationSite(stripped: string, index: number): boolean {
  const before = stripped.slice(Math.max(0, index - 24), index)
  return /\bfunction\s+$/.test(before)
}

// ---------------------------------------------------------------------------
// 前端提取
// ---------------------------------------------------------------------------

interface FrontendEndpoint {
  method: string
  template: string
  origins: string[]
}

class FrontendParseError extends Error {
  constructor(public readonly location: string, message: string) {
    super(`${location}: ${message}`)
  }
}

function unquoteString(literal: string): string {
  const quote = literal[0]
  if (quote !== "'" && quote !== '"') throw new Error(`不是字符串字面量: ${literal}`)
  const body = literal.slice(1, -1)
  return body.replace(/\\(['"\\nrt])/g, (match) => {
    switch (match[1]) {
      case 'n': return '\n'
      case 'r': return '\r'
      case 't': return '\t'
      default: return match[1]
    }
  })
}

// 模板字面量 → 规范化路径模板：${...} → {p}，截断查询串。
function normalizeTemplateText(templateBody: string): string {
  const pathPart = templateBody.split('?')[0]
  return pathPart.replace(/\$\{[^}]*\}/g, '{p}')
}

interface HelperDef {
  name: string
  params: string[]
  body: string
}

// 收集文件内返回路径的本地辅助函数（箭头函数 / function 声明）。
function collectHelperDefs(stripped: string): HelperDef[] {
  const helpers: HelperDef[] = []
  const arrowRe = /(?:const|let)\s+(\w+)\s*=\s*\(([^)]*)\)\s*(?::\s*[\w<>[\]. |]+\s*)?=>\s*(`[^`]*`|'[^']*'|"[^"]*")/g
  for (const match of stripped.matchAll(arrowRe)) {
    const params = match[2].split(',').map((p) => p.trim().split(/[:\s]/)[0]).filter(Boolean)
    helpers.push({ name: match[1], params, body: match[3] })
  }
  const funcRe = /function\s+(\w+)\s*\(([^)]*)\)\s*:\s*string\s*\{\s*return\s+(`[^`]*`|'[^']*'|"[^"]*")\s*\}/g
  for (const match of stripped.matchAll(funcRe)) {
    const params = match[2].split(',').map((p) => p.trim().split(/[:\s]/)[0]).filter(Boolean)
    helpers.push({ name: match[1], params, body: match[3] })
  }
  return helpers
}

function resolveUrlExpression(expr: string, helpers: HelperDef[], location: string): { template: string } {
  const trimmed = expr.trim()
  if (trimmed.startsWith('`')) {
    return { template: normalizeTemplateText(trimmed.slice(1, -1)) }
  }
  if ((trimmed.startsWith("'") || trimmed.startsWith('"')) && /^(['"])[\s\S]*\1$/.test(trimmed)) {
    return { template: normalizeTemplateText(unquoteString(trimmed)) }
  }
  const helperCall = /^(\w+)\s*\(([\s\S]*)\)$/.exec(trimmed)
  if (helperCall) {
    const helper = helpers.find((h) => h.name === helperCall[1])
    if (!helper) throw new FrontendParseError(location, `未知的 URL 辅助函数: ${trimmed}`)
    const args = splitTopLevel(helperCall[2], ',')
    let body = helper.body
    if (body.startsWith('`')) {
      let template = body.slice(1, -1)
      for (let p = 0; p < helper.params.length; p++) {
        const argText = args[p]?.trim() ?? ''
        const literal = argText.match(/^(['"])([\s\S]*)\1$/)
        // 字面量实参代入；变量实参所在插值段归一为 {p}。
        template = template.replace(
          new RegExp(`\\$\\{[^}]*\\b${escapeRegExp(helper.params[p])}[^}]*\\}`, 'g'),
          literal ? literal[2] : '\u0000PARAM'
        )
      }
      return { template: normalizeTemplateText(template).replaceAll('\u0000PARAM', '{p}') }
    }
    if (body.startsWith("'") || body.startsWith('"')) {
      return { template: normalizeTemplateText(unquoteString(body)) }
    }
    throw new FrontendParseError(location, `辅助函数返回值不是字符串: ${helper.name}`)
  }
  throw new FrontendParseError(location, `无法解析的 URL 表达式: ${trimmed}`)
}

// 向上（位置之前）查找 const name = `...` 的最近定义。
function findConstTemplateBefore(stripped: string, name: string, beforeIndex: number): string | undefined {
  const re = new RegExp(`(?:const|let)\\s+${escapeRegExp(name)}\\s*=\\s*(\`[^\`]*\`)`, 'g')
  let result: string | undefined
  for (const match of stripped.matchAll(re)) {
    if (match.index !== undefined && match.index < beforeIndex) result = match[1]
  }
  return result
}

function findEnclosingFunction(stripped: string, index: number): { name: string; params: string[] } | undefined {
  const re = /(?:export\s+)?(?:async\s+)?function\s+(\w+)\s*\(([^)]*)\)/g
  let current: { name: string; params: string[]; start: number } | undefined
  for (const match of stripped.matchAll(re)) {
    if (match.index !== undefined && match.index < index) {
      const params = match[2].split(',').map((p) => p.trim().split(/[:\s=]/)[0]).filter(Boolean)
      current = { name: match[1], params, start: match.index }
    }
  }
  if (!current) return undefined
  const mask = scanSource(stripped, 'ts')
  const bodyOpen = stripped.indexOf('{', current.start)
  const bodyClose = matchBracket(stripped, mask, bodyOpen)
  if (bodyOpen < 0 || bodyClose < index) return undefined
  return { name: current.name, params: current.params }
}

function collectFrontendEndpoints(): FrontendEndpoint[] {
  const endpoints = new Map<string, FrontendEndpoint>()
  const errors: FrontendParseError[] = []
  // *.test.ts 是 vitest 单测（内含 mock 调用点），不是业务端点源
  const files = listFilesRecursive(FRONTEND_API_DIR, '.ts', (name) => name.endsWith('.d.ts') || name.endsWith('.test.ts'))
  if (files.length === 0) {
    throw new Error(`前端 API 目录为空: ${FRONTEND_API_DIR}`)
  }
  const allSources = files.map((file) => {
    const raw = readFileSync(file, 'utf8')
    const stripped = stripComments(raw, 'ts')
    return { file, stripped, mask: scanSource(stripped, 'ts') }
  })

  const add = (method: string, template: string, origin: string): void => {
    const key = `${method.toUpperCase()} ${template}`
    const existing = endpoints.get(key)
    if (existing) existing.origins.push(origin)
    else endpoints.set(key, { method: method.toUpperCase(), template, origins: [origin] })
  }

  for (const { file, stripped, mask } of allSources) {
    const helpers = collectHelperDefs(stripped)

    // 1) axios 五方法调用点。
    for (const match of stripped.matchAll(/\bhttp\.(get|post|put|patch|delete)\b/g)) {
      const method = match[1]
      const callIndex = match.index + match[0].length
      const location = `${rel(file)}:${lineOfIndex(stripped, match.index)}`
      try {
        let cursor = callIndex
        while (cursor < stripped.length && /\s/.test(stripped[cursor])) cursor++
        if (stripped[cursor] === '<') {
          const genericEnd = matchBracket(stripped, mask, cursor)
          if (genericEnd > 0 && stripped[genericEnd + 1] === '(') cursor = genericEnd + 1
        }
        if (stripped[cursor] !== '(') {
          throw new FrontendParseError(location, `http.${method} 后未找到调用括号: ${stripped.slice(callIndex, callIndex + 40)}`)
        }
        const close = matchBracket(stripped, mask, cursor)
        const args = splitTopLevel(stripped.slice(cursor + 1, close), ',', mask, cursor + 1)
        if (args.length === 0) throw new FrontendParseError(location, 'http 调用无参数')
        const resolved = resolveUrlExpression(args[0], helpers, location)
        add(method, resolved.template, location)
      } catch (error) {
        if (error instanceof FrontendParseError) errors.push(error)
        else throw error
      }
    }

    // 2) fetch 调用点。
    for (const match of stripped.matchAll(/\bfetch\s*\(/g)) {
      const open = stripped.indexOf('(', match.index)
      const close = matchBracket(stripped, mask, open)
      const location = `${rel(file)}:${lineOfIndex(stripped, match.index)}`
      const args = splitTopLevel(stripped.slice(open + 1, close), ',', mask, open + 1)
      try {
        const urlExpr = args[0]?.trim() ?? ''
        const methodMatch = /method\s*:\s*['"](\w+)['"]/.exec(args[1] ?? '')
        const method = methodMatch ? methodMatch[1] : 'GET'
        const apiUrlWrap = /^apiUrl\s*\(([\s\S]*)\)$/.exec(urlExpr)
        if (apiUrlWrap) {
          const inner = apiUrlWrap[1].trim()
          if (inner.startsWith('`')) {
            add(method, normalizeTemplateText(inner.slice(1, -1)), location)
          } else if (/^\w+$/.test(inner)) {
            const template = findConstTemplateBefore(stripped, inner, match.index)
            if (template) add(method, normalizeTemplateText(template.slice(1, -1)), location)
            else throw new FrontendParseError(location, `fetch(apiUrl(${inner})) 的 ${inner} 模板定义未找到`)
          } else {
            throw new FrontendParseError(location, `fetch(apiUrl(...)) 参数无法解析: ${inner}`)
          }
        } else if (urlExpr.startsWith('`')) {
          // 形如 `${normalizeApiBaseUrl(...)}${path}${queryString(...)}`：
          // 对当前函数参数的插值引用视为参数化端点，在调用点展开实参。
          const body = urlExpr.slice(1, -1)
          const fn = findEnclosingFunction(stripped, match.index)
          const paramRefs = [...body.matchAll(/\$\{(\w+)\}/g)].map((m) => m[1])
            .filter((name) => fn?.params.includes(name))
          if (!fn || paramRefs.length === 0) {
            throw new FrontendParseError(location, `fetch URL 模板无法静态解析: ${urlExpr}`)
          }
          if (paramRefs.length > 1) {
            throw new FrontendParseError(location, `fetch URL 模板引用多个函数参数: ${paramRefs.join(', ')}`)
          }
          const pathParam = paramRefs[0]
          const paramPosition = fn.params.indexOf(pathParam)
          const callRe = new RegExp(`\\b${escapeRegExp(fn.name)}\\s*\\(`, 'g')
          const usable: Array<{ source: { file: string; stripped: string; mask: Uint8Array }; match: RegExpMatchArray }> = []
          for (const source of allSources) {
            for (const call of source.stripped.matchAll(callRe)) {
              if (call.index === undefined) continue
              if (isFunctionDeclarationSite(source.stripped, call.index)) continue
              usable.push({ source, match: call })
            }
          }
          if (usable.length === 0) {
            throw new FrontendParseError(location, `参数化 fetch 所在函数 ${fn.name} 在 api 目录内无调用点`)
          }
          for (const { source, match: call } of usable) {
            const callOpen = source.stripped.indexOf('(', call.index ?? 0)
            const callClose = matchBracket(source.stripped, source.mask, callOpen)
            const callArgs = splitTopLevel(source.stripped.slice(callOpen + 1, callClose), ',', source.mask, callOpen + 1)
            const argText = callArgs[paramPosition]?.trim()
            if (!argText || !/^['"`]/.test(argText)) {
              throw new FrontendParseError(
                `${rel(source.file)}:${lineOfIndex(source.stripped, call.index ?? 0)}`,
                `函数 ${fn.name} 第 ${paramPosition + 1} 个实参不是字面量: ${argText ?? '(缺失)'}`
              )
            }
            const resolved = resolveUrlExpression(argText, collectHelperDefs(source.stripped), location)
            add(method, resolved.template, location)
          }
        } else {
          throw new FrontendParseError(location, `fetch URL 无法解析: ${urlExpr}`)
        }
      } catch (error) {
        if (error instanceof FrontendParseError) errors.push(error)
        else throw error
      }
    }

    // 3) 独立 apiUrl(...) 调用（URL builder，GET 端点；排除 fetch 内与定义点）。
    for (const match of stripped.matchAll(/\bapiUrl\s*\(/g)) {
      if (isFunctionDeclarationSite(stripped, match.index)) continue
      const open = stripped.indexOf('(', match.index)
      const close = matchBracket(stripped, mask, open)
      const location = `${rel(file)}:${lineOfIndex(stripped, match.index)}`
      let insideFetch = false
      for (const fetchMatch of stripped.matchAll(/\bfetch\s*\(/g)) {
        const fetchOpen = stripped.indexOf('(', fetchMatch.index)
        const fetchClose = matchBracket(stripped, mask, fetchOpen)
        if (match.index > fetchOpen && match.index < fetchClose) insideFetch = true
      }
      if (insideFetch) continue
      const args = splitTopLevel(stripped.slice(open + 1, close), ',', mask, open + 1)
      try {
        const resolved = resolveUrlExpression(args[0] ?? '', helpers, location)
        add('GET', resolved.template, location)
      } catch (error) {
        if (error instanceof FrontendParseError) errors.push(error)
        else throw error
      }
    }
  }

  if (errors.length > 0) {
    assert.fail([
      '前端调用面存在无法解析的调用点（不许静默跳过）：',
      ...errors.map((e) => `  - ${e.message}`)
    ].join('\n'))
  }
  return [...endpoints.values()]
}

// ---------------------------------------------------------------------------
// Go 提取：受限表达式解析与求值
// ---------------------------------------------------------------------------

type GoNode =
  | { kind: 'str'; value: string }
  | { kind: 'num'; value: string }
  | { kind: 'ident'; parts: string[] }
  | { kind: 'call'; fnParts: string[]; args: GoNode[] }
  | { kind: 'add'; left: GoNode; right: GoNode }
  | { kind: 'unsupported'; text: string }

class GoExtractError extends Error {
  constructor(public readonly location: string, public readonly expr: string, reason: string) {
    super(`${location}: 无法解析注册 pattern 表达式 "${expr}" (${reason})`)
  }
}

function parseGoExpression(text: string): GoNode {
  const src = text.trim()
  let pos = 0

  const skipSpace = (): void => {
    while (pos < src.length && /\s/.test(src[pos])) pos++
  }

  const parsePrimary = (): GoNode => {
    skipSpace()
    if (pos >= src.length) return { kind: 'unsupported', text: src }
    const c = src[pos]
    if (c === '(') {
      pos++
      const inner = parseAdd()
      skipSpace()
      if (src[pos] === ')') pos++
      return inner
    }
    if (c === '"') {
      const m = /^"(?:[^"\\]|\\.)*"/.exec(src.slice(pos))
      if (!m) return { kind: 'unsupported', text: src }
      pos += m[0].length
      return { kind: 'str', value: goUnquote(m[0]) }
    }
    const num = /^\d[\w]*/.exec(src.slice(pos))
    if (num) {
      pos += num[0].length
      return { kind: 'num', value: num[0] }
    }
    const ident = /^[\w.]+/.exec(src.slice(pos))
    if (ident) {
      pos += ident[0].length
      const parts = ident[0].split('.')
      skipSpace()
      if (src[pos] === '(') {
        pos++
        const args: GoNode[] = []
        skipSpace()
        if (src[pos] !== ')') {
          args.push(parseAdd())
          skipSpace()
          while (src[pos] === ',') {
            pos++
            args.push(parseAdd())
            skipSpace()
          }
        }
        if (src[pos] === ')') pos++
        return { kind: 'call', fnParts: parts, args }
      }
      return { kind: 'ident', parts }
    }
    return { kind: 'unsupported', text: src }
  }

  const parseAdd = (): GoNode => {
    let left = parsePrimary()
    skipSpace()
    while (src[pos] === '+') {
      pos++
      const right = parsePrimary()
      left = { kind: 'add', left, right }
      skipSpace()
    }
    return left
  }

  const node = parseAdd()
  skipSpace()
  if (pos < src.length) return { kind: 'unsupported', text: src }
  return node
}

const HTTP_METHOD_CONSTANTS: Record<string, string> = {
  'http.MethodGet': 'GET',
  'http.MethodPost': 'POST',
  'http.MethodPut': 'PUT',
  'http.MethodPatch': 'PATCH',
  'http.MethodDelete': 'DELETE',
  'http.MethodHead': 'HEAD',
  'http.MethodOptions': 'OPTIONS'
}

// Go 双引号字符串字面量 → 值（覆盖 \x \u \U 与常见单字符转义）。
function goUnquote(literal: string): string {
  assert.ok(literal.startsWith('"') && literal.endsWith('"'), `不是 Go 字符串字面量: ${literal}`)
  const body = literal.slice(1, -1)
  let out = ''
  let i = 0
  while (i < body.length) {
    const c = body[i]
    if (c !== '\\') {
      out += c
      i++
      continue
    }
    const next = body[i + 1]
    switch (next) {
      case 'n': out += '\n'; i += 2; break
      case 'r': out += '\r'; i += 2; break
      case 't': out += '\t'; i += 2; break
      case 'a': out += '\x07'; i += 2; break
      case 'b': out += '\b'; i += 2; break
      case 'f': out += '\f'; i += 2; break
      case 'v': out += '\v'; i += 2; break
      case '\\': out += '\\'; i += 2; break
      case '"': out += '"'; i += 2; break
      case '\'': out += "'"; i += 2; break
      case 'x': {
        const hex = body.slice(i + 2, i + 4)
        out += String.fromCharCode(Number.parseInt(hex, 16))
        i += 4
        break
      }
      case 'u': {
        const hex = body.slice(i + 2, i + 6)
        out += String.fromCharCode(Number.parseInt(hex, 16))
        i += 6
        break
      }
      case 'U': {
        const hex = body.slice(i + 2, i + 10)
        out += String.fromCodePoint(Number.parseInt(hex, 16))
        i += 10
        break
      }
      case '0': case '1': case '2': case '3': case '4': case '5': case '6': case '7': {
        const oct = body.slice(i + 1, i + 4)
        out += String.fromCharCode(Number.parseInt(oct, 8))
        i += 1 + oct.length
        break
      }
      default:
        out += next
        i += 2
    }
  }
  return out
}

function goNodeStaticLiteral(node: GoNode): string | undefined {
  if (node.kind === 'str') return node.value
  if (node.kind === 'num') return node.value
  if (node.kind === 'ident' && node.parts.length > 1) {
    const constant = HTTP_METHOD_CONSTANTS[node.parts.join('.')]
    if (constant) return constant
  }
  if (node.kind === 'add') {
    const left = goNodeStaticLiteral(node.left)
    const right = goNodeStaticLiteral(node.right)
    if (left !== undefined && right !== undefined) return left + right
  }
  return undefined
}

// ---------------------------------------------------------------------------
// Go 提取：文件/函数/绑定索引
// ---------------------------------------------------------------------------

interface ElementBinding {
  fields: string[]
  elements: Array<{ exprs: Map<string, string> }>  // 字段名 → 表达式文本（惰性求值）
}

interface GoClosure {
  name: string
  params: string[]
  bodyStart: number // 在文件 stripped 中的绝对偏移
  bodyEnd: number
  // 每个调用点一组的实参绑定（仅收集可求值的标量参数；同一调用点内
  // 的参数组合保持配对，不做跨调用点全组合）。
  calls: Array<{ args: Map<string, string>; location: string }>
}

interface GoRangeLoop {
  varName: string
  headerStart: number // for 关键字在函数体内的偏移
  bodyStart: number // 循环体 { 在文件 stripped 中的绝对偏移
  bodyEnd: number
  rangeExpr: string
  resolvedBinding?: ElementBinding
}

interface GoFunc {
  name: string
  params: string[]
  start: number
  bodyStart: number
  end: number
  assignments: Map<string, string[]>
  closures: GoClosure[]
  rangeLoops: GoRangeLoop[]
}

interface GoFileInfo {
  path: string
  stripped: string
  mask: Uint8Array
  funcs: GoFunc[]
  pkgConsts: Map<string, string>
}

function parseGoFile(path: string): GoFileInfo {
  const raw = readFileSync(path, 'utf8')
  const stripped = stripComments(raw, 'go')
  const mask = scanSource(stripped, 'go')
  const pkgConsts = new Map<string, string>()

  for (const match of stripped.matchAll(/^const\s+(\w+)\s*=\s*("[^"]*")/gm)) {
    pkgConsts.set(match[1], goUnquote(match[2]))
  }
  for (const block of stripped.matchAll(/^const\s*\(([\s\S]*?)^\)/gm)) {
    for (const line of block[1].matchAll(/(\w+)\s*=\s*("[^"]*")/g)) {
      pkgConsts.set(line[1], goUnquote(line[2]))
    }
  }

  const funcs: GoFunc[] = []
  const funcRe = /^func\s+(?:\([^)]*\)\s*)?(\w+)\s*\(([^)]*)\)/gm
  for (const match of stripped.matchAll(funcRe)) {
    const name = match[1]
    const params = match[2].split(',').map((p) => p.trim().split(/\s+/)[0]).filter((p) => p && !p.startsWith('('))
    const bodyStart = stripped.indexOf('{', match.index + match[0].length)
    if (bodyStart < 0) continue
    const bodyEnd = matchBracket(stripped, mask, bodyStart)
    if (bodyEnd < 0) continue
    const bodyOffset = bodyStart + 1
    const body = stripped.slice(bodyOffset, bodyEnd)
    const fn: GoFunc = {
      name,
      params,
      start: match.index,
      bodyStart,
      end: bodyEnd,
      assignments: new Map(),
      closures: [],
      rangeLoops: []
    }
    funcs.push(fn)

    // 函数内赋值绑定（RHS 括号平衡截取，支持跨行；for 头部的 := 由此产生
    // 的伪绑定会被 range 元素解析优先级屏蔽）。
    const assignRe = /^([ \t]*)(\w+)\s*:?=[^=]/gm
    for (const assign of body.matchAll(assignRe)) {
      const varName = assign[2]
      const rhsStart = assign.index + assign[0].length
      let rhsEnd = rhsStart
      let depth = 0
      while (rhsEnd < body.length) {
        const m = mask[bodyOffset + rhsEnd]
        const ch = body[rhsEnd]
        if (m === MASK_CODE) {
          if (ch === '(' || ch === '{' || ch === '[') depth++
          else if (ch === ')' || ch === '}' || ch === ']') {
            if (depth === 0) break
            depth--
          } else if (ch === '\n' && depth === 0) break
          else if (ch === ',' && depth === 0) break
        }
        rhsEnd++
      }
      const rhs = body.slice(rhsStart, rhsEnd).trim()
      if (rhs.length > 0) {
        const list = fn.assignments.get(varName) ?? []
        list.push(rhs)
        fn.assignments.set(varName, list)
      }
    }

    // range 循环：括号感知解析（区分 composite literal 与简单表达式），
    // 统一使用 stripped 绝对偏移。
    for (const rangeMatch of body.matchAll(/\bfor\s+(?:_\s*,\s*)?(\w+)\s*:=\s*range\s/g)) {
      const varName = rangeMatch[1]
      const exprStartAbs = bodyOffset + rangeMatch.index + rangeMatch[0].length
      let cursorAbs = exprStartAbs
      while (cursorAbs < stripped.length && /\s/.test(stripped[cursorAbs])) cursorAbs++
      const isComposite = stripped.slice(cursorAbs, cursorAbs + 2) === '[]'
      let rangeExprEndAbs = -1
      let loopBodyOpenAbs = -1
      if (isComposite) {
        // []struct{...}{...} {：前两个顶层 {...} 属于表达式，第三个是循环体。
        let topBraces = 0
        let firstBraceAbs = -1
        let secondBraceEndAbs = -1
        let scanAbs = cursorAbs
        while (scanAbs < stripped.length) {
          if (mask[scanAbs] !== MASK_CODE) {
            scanAbs++
            continue
          }
          const ch = stripped[scanAbs]
          if (ch === '{' && isTopLevelOpenAbs(stripped, mask, cursorAbs, scanAbs)) {
            topBraces++
            if (topBraces === 1) firstBraceAbs = scanAbs
            if (topBraces === 2) {
              secondBraceEndAbs = matchBracket(stripped, mask, scanAbs)
              break
            }
          }
          scanAbs++
        }
        if (firstBraceAbs < 0 || secondBraceEndAbs < 0) continue
        rangeExprEndAbs = secondBraceEndAbs + 1
        let afterAbs = rangeExprEndAbs
        while (afterAbs < stripped.length && /\s/.test(stripped[afterAbs])) afterAbs++
        if (stripped[afterAbs] === '{') loopBodyOpenAbs = afterAbs
      } else {
        // 简单表达式：第一个顶层 { 即循环体。
        let scanAbs = cursorAbs
        while (scanAbs < stripped.length) {
          if (mask[scanAbs] !== MASK_CODE) {
            scanAbs++
            continue
          }
          if (stripped[scanAbs] === '{') break
          scanAbs++
        }
        rangeExprEndAbs = scanAbs
        loopBodyOpenAbs = scanAbs
      }
      if (rangeExprEndAbs < 0 || loopBodyOpenAbs < 0) continue
      const bodyCloseAbs = matchBracket(stripped, mask, loopBodyOpenAbs)
      fn.rangeLoops.push({
        varName,
        headerStart: rangeMatch.index,
        bodyStart: loopBodyOpenAbs,
        bodyEnd: bodyCloseAbs,
        rangeExpr: stripped.slice(cursorAbs, rangeExprEndAbs).trim()
      })
    }

    // 注册闭包：name := func(params) { ... k.Register ... }
    const closureRe = /(\w+)\s*:=\s*func\s*\(([^)]*)\)\s*(?:[^\s{][^{]*?)?\{/g
    for (const closureMatch of body.matchAll(closureRe)) {
      const braceOpenAbs = bodyOffset + body.indexOf('{', closureMatch.index + closureMatch[0].length - 1)
      const braceEndAbs = matchBracket(stripped, mask, braceOpenAbs)
      const closureBody = stripped.slice(braceOpenAbs + 1, braceEndAbs)
      if (!/\b(?:k|kern)\.(Register|RegisterFunc)\s*\(/.test(closureBody)) continue
      const params = closureMatch[2].split(',').map((p) => p.trim().split(/\s+/)[0]).filter(Boolean)
      fn.closures.push({
        name: closureMatch[1],
        params,
        bodyStart: braceOpenAbs,
        bodyEnd: braceEndAbs,
        calls: []
      })
    }
  }

  return { path, stripped, mask, funcs, pkgConsts }
}

// 判断 stripped[at] 处的 { 是否位于 from..at 区间的顶层（深度 0）。
function isTopLevelOpenAbs(stripped: string, mask: Uint8Array, from: number, at: number): boolean {
  let depth = 0
  for (let i = from; i < at; i++) {
    if (mask[i] !== MASK_CODE) continue
    const ch = stripped[i]
    if (ch === '(' || ch === '{' || ch === '[') depth++
    else if (ch === ')' || ch === '}' || ch === ']') depth--
  }
  return depth === 0
}

// ---------------------------------------------------------------------------
// Go 提取：range 表达式 → 元素绑定
// ---------------------------------------------------------------------------

interface FuncRef {
  file: GoFileInfo
  func: GoFunc
}

class GoPackageIndex {
  readonly funcsByName = new Map<string, FuncRef[]>()
  readonly files: GoFileInfo[] = []
  readonly constsByDir = new Map<string, Map<string, string>>()

  addFile(info: GoFileInfo, dir: string): void {
    this.files.push(info)
    for (const fn of info.funcs) {
      const list = this.funcsByName.get(fn.name) ?? []
      list.push({ file: info, func: fn })
      this.funcsByName.set(fn.name, list)
    }
    const consts = this.constsByDir.get(dir) ?? new Map<string, string>()
    for (const [key, value] of info.pkgConsts) consts.set(key, value)
    this.constsByDir.set(dir, consts)
  }

  findFuncRef(name: string): FuncRef | undefined {
    const list = this.funcsByName.get(name)
    if (!list || list.length !== 1) return undefined
    return list[0]
  }
}

// 解析 []struct{...}{...} composite literal（元素可为位置或键值形态）。
function parseStructSliceLiteral(expr: string): ElementBinding {
  const localMask = scanSource(expr, 'go')
  const typeDeclMatch = /\[\]\s*struct\s*\{/.exec(expr)
  if (!typeDeclMatch) throw new Error(`range 表达式不是 []struct composite literal: ${expr.slice(0, 60)}`)
  const typeOpen = typeDeclMatch.index + typeDeclMatch[0].length - 1
  const typeClose = matchBracket(expr, localMask, typeOpen)
  const typeBody = expr.slice(typeOpen + 1, typeClose)
  const fields = [...typeBody.matchAll(/(\w+)\s+[\w.[\]*]+\b/g)].map((m) => m[1])
  // 元素列表：类型声明闭括号之后的第一个顶层 {。
  let scan = typeClose + 1
  while (scan < expr.length && /\s/.test(expr[scan])) scan++
  if (expr[scan] !== '{') throw new Error(`range composite literal 元素列表未找到: ${expr.slice(0, 60)}`)
  const elementsOpen = scan
  const elementsClose = matchBracket(expr, localMask, elementsOpen)
  const elementsText = expr.slice(elementsOpen + 1, elementsClose)
  const binding: ElementBinding = { fields, elements: [] }
  for (const elementText of splitTopLevel(elementsText, ',', localMask, elementsOpen + 1)) {
    if (!elementText.startsWith('{')) continue
    const innerOffset = elementsOpen + 1 + elementsText.indexOf(elementText)
    const inner = elementText.slice(1, -1)
    const exprs = new Map<string, string>()
    const parts = splitTopLevel(inner, ',', localMask, innerOffset + 1)
    parts.forEach((part, index) => {
      const kv = /^\s*(\w+)\s*:\s*([\s\S]+)$/.exec(part)
      if (kv) {
        exprs.set(kv[1], kv[2].trim())
      } else if (index < fields.length) {
        exprs.set(fields[index], part)
      }
    })
    binding.elements.push({ exprs })
  }
  return binding
}

// range 表达式为 composite literal、函数内变量（其赋值为 composite
// literal）或无参函数调用（函数返回构造函数列表）。
function resolveRangeBinding(rangeExpr: string, env: EvalEnv): ElementBinding {
  const expr = rangeExpr.trim()
  if (expr.startsWith('[]struct')) {
    return parseStructSliceLiteral(expr)
  }
  const varMatch = /^(\w+)$/.exec(expr)
  if (varMatch) {
    const rhsList = env.fn.assignments.get(varMatch[1])?.filter((rhs) => rhs.startsWith('[]struct'))
    if (rhsList && rhsList.length === 1) {
      return parseStructSliceLiteral(rhsList[0])
    }
  }
  const callMatch = /^(\w+)\(\)$/.exec(expr)
  if (!callMatch) throw new Error(`不支持的 range 表达式: ${expr}`)
  const pkg = env.pkg
  const producer = pkg.findFuncRef(callMatch[1])
  if (!producer) throw new Error(`range 函数 ${callMatch[1]}() 未找到唯一定义`)
  const producerText = producer.file.stripped.slice(producer.func.bodyStart, producer.func.end)
  const returnMatch = /\breturn\s+\[\]\w+\s*\{/.exec(producerText)
  if (!returnMatch) throw new Error(`range 函数 ${callMatch[1]}() 的 return composite literal 未找到`)
  const listOpen = producer.func.bodyStart + returnMatch.index + returnMatch[0].length - 1
  const listClose = matchBracket(producer.file.stripped, producer.file.mask, listOpen)
  const listText = producer.file.stripped.slice(listOpen + 1, listClose)
  const elementCalls = splitTopLevel(listText, ',', producer.file.mask, listOpen + 1)
  if (elementCalls.length === 0 || !elementCalls.every((e) => /^\w+\(\)$/.test(e))) {
    throw new Error(`range 函数 ${callMatch[1]}() 返回元素不是构造函数调用: ${listText.slice(0, 80)}`)
  }
  const binding: ElementBinding = { fields: [], elements: [] }
  for (const elementCall of elementCalls) {
    const constructor = pkg.findFuncRef(elementCall.slice(0, -2))
    if (!constructor) throw new Error(`plan 构造函数 ${elementCall} 未找到唯一定义`)
    const ctorText = constructor.file.stripped.slice(constructor.func.bodyStart, constructor.func.end)
    const literalMatch = /\b(\w+)\s*:?=\s*\w+\s*\{/.exec(ctorText)
    if (!literalMatch) throw new Error(`plan 构造函数 ${elementCall} 的 struct literal 未找到`)
    const literalOpen = constructor.func.bodyStart + literalMatch.index + literalMatch[0].length - 1
    const literalClose = matchBracket(constructor.file.stripped, constructor.file.mask, literalOpen)
    const literalText = constructor.file.stripped.slice(literalOpen + 1, literalClose)
    const exprs = new Map<string, string>()
    for (const field of splitTopLevel(literalText, ',', constructor.file.mask, literalOpen + 1)) {
      const kv = /^\s*(\w+)\s*:\s*("[^"]*"|true|false)\s*$/.exec(field)
      if (kv) exprs.set(kv[1], kv[2])
    }
    binding.elements.push({ exprs })
  }
  return binding
}

// ---------------------------------------------------------------------------
// Go 提取：求值环境
// ---------------------------------------------------------------------------

interface EvalEnv {
  file: GoFileInfo
  fn: GoFunc
  pkg: GoPackageIndex
  dir: string
  pinnedElements: Map<string, { exprs: Map<string, string> }>
  pinnedValues: Map<string, string>
  depth: number
}

function evalGoPattern(exprText: string, env: EvalEnv): string[] {
  const node = parseGoExpression(exprText)
  if (node.kind === 'unsupported') {
    throw new GoExtractError(`${rel(env.file.path)}:${lineOfIndex(env.file.stripped, env.fn.bodyStart)}`, exprText, '表达式语法超出受限求值器能力')
  }
  return evalNode(node, env)
}

function collectElementVarRefs(node: GoNode, env: EvalEnv, found: Set<string>): void {
  if (node.kind === 'add') {
    collectElementVarRefs(node.left, env, found)
    collectElementVarRefs(node.right, env, found)
  } else if (node.kind === 'ident') {
    if (resolveElementBinding(node.parts[0], env)) found.add(node.parts[0])
  } else if (node.kind === 'call') {
    for (const arg of node.args) collectElementVarRefs(arg, env, found)
  }
}

function evalNode(node: GoNode, env: EvalEnv): string[] {
  if (node.kind === 'str') return [node.value]
  if (node.kind === 'num') return [node.value]
  if (node.kind === 'unsupported') {
    throw new GoExtractError(rel(env.file.path), node.text, '不支持的表达式节点')
  }
  if (node.kind === 'ident') {
    return evalIdent(node.parts, env)
  }
  if (node.kind === 'call') {
    return evalCall(node.fnParts, node.args, env)
  }
  // add：存在元素变量引用时按元素分支（同一变量的多次字段访问保持同元素配对）。
  const elementVars = new Set<string>()
  collectElementVarRefs(node, env, elementVars)
  if (elementVars.size === 0) {
    const left = evalNode(node.left, env)
    const right = evalNode(node.right, env)
    const out: string[] = []
    for (const l of left) for (const r of right) out.push(l + r)
    return out
  }
  const varNames = [...elementVars]
  const combos: Array<Array<{ exprs: Map<string, string> }>> = []
  const buildCombo = (index: number, current: Array<{ exprs: Map<string, string> }>): void => {
    if (index === varNames.length) {
      combos.push([...current])
      return
    }
    const binding = resolveElementBinding(varNames[index], env)
    if (!binding) return
    for (const element of binding.elements) {
      current.push(element)
      buildCombo(index + 1, current)
      current.pop()
    }
  }
  buildCombo(0, [])
  const out: string[] = []
  for (const combo of combos) {
    const pinned = new Map(env.pinnedElements)
    varNames.forEach((name, i) => pinned.set(name, combo[i]))
    const branchEnv: EvalEnv = { ...env, pinnedElements: pinned }
    const left = evalNode(node.left, branchEnv)
    const right = evalNode(node.right, branchEnv)
    for (const l of left) for (const r of right) out.push(l + r)
  }
  return out
}

// 标识符 → 元素绑定（pin 或当前函数内覆盖该位置的 range 循环变量）。
function resolveElementBinding(name: string, env: EvalEnv): ElementBinding | undefined {
  const pinned = env.pinnedElements.get(name)
  if (pinned) return { fields: [], elements: [pinned] }
  for (const loop of env.fn.rangeLoops) {
    if (loop.varName !== name) continue
    // 需要位置校验：求值点应位于循环体内。调用方传入的位置信息不足时
    // 按函数内唯一循环变量名处理（当前代码库满足）。
    if (!loop.resolvedBinding) {
      loop.resolvedBinding = resolveRangeBinding(loop.rangeExpr, env)
    }
    return loop.resolvedBinding
  }
  const paramBinding = resolveParamBinding(name, env)
  if (paramBinding?.kind === 'elements') return paramBinding.binding
  return undefined
}

type ParamBinding =
  | { kind: 'values'; values: string[] }
  | { kind: 'elements'; binding: ElementBinding }

function resolveParamBinding(name: string, env: EvalEnv): ParamBinding | undefined {
  if (!env.fn.params.includes(name)) return undefined
  // 默认值模式优先：函数体内对参数有赋值（如 chat Register 的 prefix 默认值）。
  const assignments = env.fn.assignments.get(name)?.filter((rhs) => !rhs.startsWith('range '))
  if (assignments && assignments.length > 0) {
    const values: string[] = []
    for (const rhs of assignments) values.push(...evalGoPattern(rhs, env))
    return { kind: 'values', values }
  }
  if (env.depth >= 4) {
    throw new GoExtractError(rel(env.file.path), name, '参数代入深度超限（可能递归）')
  }
  // 查调用点（要求函数名全局唯一，避免与 kernel.Register 等多义名混淆）。
  const def = env.pkg.findFuncRef(env.fn.name)
  if (!def || def.func !== env.fn) return undefined
  const paramPosition = env.fn.params.indexOf(name)
  const callRe = new RegExp(`\\.${escapeRegExp(env.fn.name)}\\s*\\(`, 'g')
  const values: string[] = []
  const elementBindings: ElementBinding[] = []
  let foundCall = false
  for (const candidate of env.pkg.files) {
    for (const call of candidate.stripped.matchAll(callRe)) {
      const open = candidate.stripped.indexOf('(', call.index)
      const close = matchBracket(candidate.stripped, candidate.mask, open)
      if (close < 0) continue
      const args = splitTopLevel(candidate.stripped.slice(open + 1, close), ',', candidate.mask, open + 1)
      const argText = args[paramPosition]
      if (!argText) continue
      foundCall = true
      const callerFn = candidate.funcs.find((f) => f.bodyStart < open && f.end > close)
      if (!callerFn) continue
      const callerEnv: EvalEnv = {
        file: candidate,
        fn: callerFn,
        pkg: env.pkg,
        dir: env.dir,
        pinnedElements: new Map(),
        pinnedValues: new Map(),
        depth: env.depth + 1
      }
      const trimmedArg = argText.trim()
      if (/^\w+$/.test(trimmedArg)) {
        const elementBinding = resolveElementBinding(trimmedArg, callerEnv)
        if (elementBinding) {
          elementBindings.push(elementBinding)
          continue
        }
      }
      try {
        values.push(...evalGoPattern(trimmedArg, callerEnv))
      } catch (error) {
        if (error instanceof GoExtractError) continue // 该调用点不可解，其他调用点仍可贡献
        throw error
      }
    }
  }
  if (!foundCall) return undefined
  if (elementBindings.length > 0) {
    return { kind: 'elements', binding: { fields: [], elements: elementBindings.flatMap((b) => b.elements) } }
  }
  if (values.length > 0) return { kind: 'values', values }
  return undefined
}

function evalIdent(parts: string[], env: EvalEnv): string[] {
  const dotted = parts.join('.')
  if (parts.length > 1) {
    if (HTTP_METHOD_CONSTANTS[dotted]) return [HTTP_METHOD_CONSTANTS[dotted]]
    const binding = resolveElementBinding(parts[0], env)
    if (binding) {
      const out: string[] = []
      for (const element of binding.elements) {
        const expr = element.exprs.get(parts[1])
        if (expr === undefined) {
          throw new GoExtractError(rel(env.file.path), dotted, `元素字段 ${parts[1]} 无法静态求值`)
        }
        out.push(...evalGoPattern(expr, env))
      }
      return out
    }
    throw new GoExtractError(rel(env.file.path), dotted, '无法解析的限定标识符')
  }
  const name = parts[0]
  const pinnedValue = env.pinnedValues.get(name)
  if (pinnedValue !== undefined) return [pinnedValue]
  if (env.pinnedElements.has(name)) {
    throw new GoExtractError(rel(env.file.path), name, 'range 元素变量需通过字段访问使用')
  }
  // range 元素绑定优先于赋值伪绑定（for 头部 := 产生的 range 残片）。
  if (resolveElementBinding(name, env)) {
    // 单独引用元素变量本身不构成 pattern 片段；视为不可求值。
    throw new GoExtractError(rel(env.file.path), name, 'range 元素变量需通过字段访问使用')
  }
  const assignments = env.fn.assignments.get(name)?.filter((rhs) => !rhs.startsWith('range '))
  if (assignments && assignments.length > 0) {
    const out: string[] = []
    for (const rhs of assignments) out.push(...evalGoPattern(rhs, env))
    return out
  }
  const param = resolveParamBinding(name, env)
  if (param?.kind === 'values') return param.values
  const consts = env.pkg.constsByDir.get(env.dir)
  if (consts?.has(name)) return [consts.get(name) as string]
  throw new GoExtractError(rel(env.file.path), name, '标识符无法解析为常量字符串')
}

function evalCall(fnParts: string[], args: GoNode[], env: EvalEnv): string[] {
  const fnName = fnParts.join('.')
  if (fnName === 'strings.Replace') {
    if (args.length !== 4) {
      throw new GoExtractError(rel(env.file.path), fnName, 'strings.Replace 参数数量不符')
    }
    const evaluated = args.map((a) => {
      const values = evalNode(a, env)
      if (values.length !== 1) {
        throw new GoExtractError(rel(env.file.path), fnName, 'strings.Replace 参数含多分支值')
      }
      return values[0]
    })
    const [target, from, to] = evaluated
    return [target.replace(from, to)]
  }
  throw new GoExtractError(rel(env.file.path), fnName, '不支持的函数调用')
}

// ---------------------------------------------------------------------------
// Go 提取：注册调用点收集
// ---------------------------------------------------------------------------

interface GoRoute {
  method: string // ANY 表示无方法前缀的 subtree 挂载
  template: string
  surface: string
  origins: string[]
}

function classifySurface(template: string): string {
  if (template === SYSTEM_API_PREFIX || template.startsWith(SYSTEM_API_PREFIX + '/')) return 'aisys'
  if (template === '/v1' || template.startsWith('/v1/')) return 'v1'
  if (template.startsWith('/__aipublic__')) return 'aipublic'
  if (template === '/__aisys__' || template.startsWith('/__aisys__/')) return 'management-static'
  return 'other'
}

function parsePatternString(pattern: string, location: string): { method: string; template: string } {
  const spaceIndex = pattern.indexOf(' ')
  if (spaceIndex < 0) return { method: 'ANY', template: pattern }
  const maybeMethod = pattern.slice(0, spaceIndex)
  if (/^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)$/.test(maybeMethod)) {
    return { method: maybeMethod, template: pattern.slice(spaceIndex + 1) }
  }
  throw new GoExtractError(location, pattern, 'pattern 方法段无法识别')
}

function collectGoRoutes(): { routes: GoRoute[]; failures: GoExtractError[] } {
  const goFiles = GO_SCAN_ROOTS.flatMap((root) =>
    listFilesRecursive(root, '.go', (name) => name.endsWith('_test.go'))
  )
  if (goFiles.length === 0) throw new Error(`Go 扫描目录为空: ${GO_SCAN_ROOTS.join(', ')}`)
  const pkg = new GoPackageIndex()
  for (const file of goFiles) {
    const info = parseGoFile(file)
    const posixPath = file.replaceAll('\\', '/')
    pkg.addFile(info, posixPath.slice(0, posixPath.lastIndexOf('/')))
  }

  const routes: GoRoute[] = []
  const failures: GoExtractError[] = []
  const registerRe = /\b(?:k|kern)\.(Register|RegisterFunc)\s*\(/g

  for (const info of pkg.files) {
    for (const fn of info.funcs) {
      const baseEnv: EvalEnv = {
        file: info,
        fn,
        pkg,
        dir: dirOf(info),
        pinnedElements: new Map(),
        pinnedValues: new Map(),
        depth: 0
      }

      // 闭包调用点实参代入：mount("GET", "/path", handler)。
      for (const closure of fn.closures) {
        const body = info.stripped.slice(fn.bodyStart + 1, fn.end)
        const callRe = new RegExp(`\\b${escapeRegExp(closure.name)}\\s*\\(`, 'g')
        for (const call of body.matchAll(callRe)) {
          const open = info.stripped.indexOf('(', fn.bodyStart + 1 + call.index)
          const close = matchBracket(info.stripped, info.mask, open)
          if (close < 0) continue
          const callLocation = `${rel(info.path)}:${lineOfIndex(info.stripped, open)}`
          const args = splitTopLevel(info.stripped.slice(open + 1, close), ',', info.mask, open + 1)
          const binding = new Map<string, string>()
          closure.params.forEach((param, position) => {
            const argText = args[position]?.trim()
            if (!argText) return
            try {
              const node = parseGoExpression(argText)
              const literal = goNodeStaticLiteral(node)
              const values = literal !== undefined ? [literal] : evalNode(node, baseEnv)
              if (values.length === 1) binding.set(param, values[0])
            } catch {
              // handler 实参等不可求值参数忽略。
            }
          })
          closure.calls.push({ args: binding, location: callLocation })
        }
      }

      const fnText = info.stripped.slice(fn.bodyStart, fn.end)
      for (const match of fnText.matchAll(registerRe)) {
        const absoluteIndex = fn.bodyStart + match.index
        const location = `${rel(info.path)}:${lineOfIndex(info.stripped, absoluteIndex)}`
        const inClosure = fn.closures.some((c) => absoluteIndex > c.bodyStart && absoluteIndex < c.bodyEnd)
        const open = info.stripped.indexOf('(', absoluteIndex)
        const close = matchBracket(info.stripped, info.mask, open)
        if (close < 0) {
          failures.push(new GoExtractError(location, info.stripped.slice(absoluteIndex, absoluteIndex + 60), '调用括号不闭合'))
          continue
        }
        const args = splitTopLevel(info.stripped.slice(open + 1, close), ',', info.mask, open + 1)
        const patternExpr = args[0] ?? ''
        try {
          let patterns: string[]
          if (inClosure) {
            const closure = fn.closures.find((c) => absoluteIndex > c.bodyStart && absoluteIndex < c.bodyEnd) as GoClosure
            patterns = evalWithClosureParams(patternExpr, closure, baseEnv, location)
          } else {
            patterns = evalGoPattern(patternExpr, baseEnv)
          }
          for (const pattern of patterns) {
            const parsed = parsePatternString(pattern, location)
            routes.push({ ...parsed, surface: classifySurface(parsed.template), origins: [location] })
          }
        } catch (error) {
          if (error instanceof GoExtractError) failures.push(error)
          else failures.push(new GoExtractError(location, patternExpr, String(error)))
        }
      }
    }
  }
  return { routes, failures }
}

function dirOf(info: GoFileInfo): string {
  const posixPath = info.path.replaceAll('\\', '/')
  return posixPath.slice(0, posixPath.lastIndexOf('/'))
}

function evalWithClosureParams(expr: string, closure: GoClosure, outerEnv: EvalEnv, location: string): string[] {
  if (closure.calls.length === 0) {
    throw new GoExtractError(location, `${closure.name}(...)`, '注册闭包在本函数内没有可解析的调用点')
  }
  const out: string[] = []
  for (const call of closure.calls) {
    out.push(...evalGoPattern(expr, { ...outerEnv, pinnedValues: new Map(call.args) }))
  }
  return out
}

// ---------------------------------------------------------------------------
// Go 提取：J3b model-checks 独立链路
// ---------------------------------------------------------------------------

function collectJ3bRoutes(): { routes: GoRoute[]; failures: GoExtractError[] } {
  const routes: GoRoute[] = []
  const failures: GoExtractError[] = []
  const mainPath = join(GO_SCAN_ROOTS[1], 'main.go')
  const hostPath = join(GO_SCAN_ROOTS[0], 'modelcheckowner', 'host.go')
  const httpPath = join(GO_SCAN_ROOTS[0], 'modelcheckowner', 'http.go')
  if (!existsSync(mainPath) || !existsSync(hostPath) || !existsSync(httpPath)) {
    assert.fail(`J3b 链路源码缺失: ${mainPath} / ${hostPath} / ${httpPath}`)
  }
  const mainStripped = stripComments(readFileSync(mainPath, 'utf8'), 'go')

  const prefixes: Array<{ prefix: string; location: string }> = []
  for (const match of mainStripped.matchAll(/j3bHost\.MountScoped\(\s*[^,]+,\s*"(\/[^"]*)"/g)) {
    prefixes.push({ prefix: match[1], location: `${rel(mainPath)}:${lineOfIndex(mainStripped, match.index)}` })
  }
  const managementPrefixes: string[] = []
  for (const match of mainStripped.matchAll(/j3bHost\.Mount\(\s*[^,]+,\s*"(\/[^"]*)"/g)) {
    managementPrefixes.push(match[1])
  }
  if (prefixes.length === 0) {
    failures.push(new GoExtractError(rel(mainPath), 'j3bHost.MountScoped(...)', '未找到 MountScoped 调用（J3b 链路可能变更）'))
    return { routes, failures }
  }

  // host.go 验证 MountScoped/Mount 将 prefix 直接 mux.Handle（不追加路径）。
  const hostStripped = stripComments(readFileSync(hostPath, 'utf8'), 'go')
  const hostMask = scanSource(hostStripped, 'go')
  for (const fnName of ['MountScoped', 'Mount']) {
    const fnRe = new RegExp(`func \\(h \\*Host\\) ${fnName}\\(`)
    const fnMatch = fnRe.exec(hostStripped)
    const fnBodyOpen = fnMatch ? hostStripped.indexOf('{', fnMatch.index)
      : -1
    const fnBodyClose = fnBodyOpen >= 0 ? matchBracket(hostStripped, hostMask, fnBodyOpen) : -1
    if (fnBodyClose < 0 || !/mux\.Handle\(\s*prefix/.test(hostStripped.slice(fnBodyOpen, fnBodyClose))) {
      failures.push(new GoExtractError(rel(hostPath), `(*Host).${fnName}`, '未确认 mux.Handle(prefix, ...) 直接挂载形态'))
    }
  }

  // http.go ServeHTTP switch 解析。
  const httpStripped = stripComments(readFileSync(httpPath, 'utf8'), 'go')
  const httpMask = scanSource(httpStripped, 'go')
  const serveMatch = /func \(h \*HTTPHandler\) ServeHTTP\(/.exec(httpStripped)
  if (!serveMatch) {
    failures.push(new GoExtractError(rel(httpPath), '(*HTTPHandler).ServeHTTP', '函数未找到'))
    return { routes, failures }
  }
  const serveBodyOpen = httpStripped.indexOf('{', serveMatch.index)
  const serveBodyClose = matchBracket(httpStripped, httpMask, serveBodyOpen)
  const serveBody = httpStripped.slice(serveBodyOpen, serveBodyClose)
  interface J3bCase { method: string; paths: Array<{ path: string; prefix: boolean }> }
  const cases: J3bCase[] = []
  for (const caseMatch of serveBody.matchAll(/case\s+([^:]+):/g)) {
    const condition = caseMatch[1].trim()
    const methodMatches = [...condition.matchAll(/r\.Method\s*==\s*http\.Method(\w+)/g)].map((m) => m[1].toUpperCase())
    if (methodMatches.length !== 1) continue
    const paths: Array<{ path: string; prefix: boolean }> = []
    for (const eq of condition.matchAll(/path\s*==\s*"([^"]+)"/g)) {
      paths.push({ path: eq[1], prefix: false })
    }
    for (const hp of condition.matchAll(/strings\.HasPrefix\(\s*path\s*,\s*"([^"]+)"/g)) {
      paths.push({ path: hp[1], prefix: true })
    }
    if (paths.length === 0) continue
    cases.push({ method: methodMatches[0], paths })
  }
  if (cases.length === 0) {
    failures.push(new GoExtractError(rel(httpPath), 'ServeHTTP switch', '未解析到任何 case 条件'))
  }

  for (const { prefix, location } of prefixes) {
    const base = prefix.endsWith('/') ? prefix.slice(0, -1) : prefix
    for (const j3bCase of cases) {
      for (const pathInfo of j3bCase.paths) {
        routes.push({
          method: j3bCase.method,
          template: pathInfo.prefix ? `${base}${pathInfo.path}{p}` : `${base}${pathInfo.path}`,
          surface: 'j3b',
          origins: [`${location} + ${rel(httpPath)} ServeHTTP`]
        })
      }
    }
  }
  // J3b 独立 management 面（不带 /__aisys__/api 前缀，前端不调用）仅计数。
  for (const prefix of managementPrefixes) {
    const base = prefix.endsWith('/') ? prefix.slice(0, -1) : prefix
    for (const j3bCase of cases) {
      for (const pathInfo of j3bCase.paths) {
        routes.push({
          method: j3bCase.method,
          template: pathInfo.prefix ? `${base}${pathInfo.path}{p}` : `${base}${pathInfo.path}`,
          surface: 'j3b-management',
          origins: [`${rel(mainPath)} j3bHost.Mount(${prefix})`]
        })
      }
    }
  }
  return { routes, failures }
}

// ---------------------------------------------------------------------------
// 比对
// ---------------------------------------------------------------------------

interface AllowlistEntry {
  template: string
  reason: string
}

function normalizeGoTemplateForMatch(template: string): string {
  return template.replace(/\{[^{/}]+\}/g, '{p}')
}

function templateMatches(frontendTemplate: string, goTemplate: string): boolean {
  const normalizedGo = normalizeGoTemplateForMatch(goTemplate)
  if (frontendTemplate === normalizedGo) return true
  if (normalizedGo.endsWith('/') && frontendTemplate.startsWith(normalizedGo)) return true
  return false
}

function levenshtein(a: string, b: string): number {
  const dp: number[][] = Array.from({ length: a.length + 1 }, (_, i) => [i, ...Array(b.length).fill(0)])
  for (let j = 0; j <= b.length; j++) dp[0][j] = j
  for (let i = 1; i <= a.length; i++) {
    for (let j = 1; j <= b.length; j++) {
      dp[i][j] = Math.min(
        dp[i - 1][j] + 1,
        dp[i][j - 1] + 1,
        dp[i - 1][j - 1] + (a[i - 1] === b[j - 1] ? 0 : 1)
      )
    }
  }
  return dp[a.length][b.length]
}

function main(): void {
  const frontend = collectFrontendEndpoints()
  const { routes: goRoutes, failures: goFailures } = collectGoRoutes()
  const j3b = collectJ3bRoutes()
  const allFailures = [...goFailures, ...j3b.failures]
  if (allFailures.length > 0) {
    assert.fail([
      'Go 注册面存在无法解析的注册点（不许静默跳过导致假绿）：',
      ...allFailures.map((f) => `  - ${f.message}`)
    ].join('\n'))
  }

  const assertable = [...goRoutes, ...j3b.routes]
    .filter((r) => r.surface === 'aisys' || r.surface === 'j3b')
    .map((r) => {
      // 与前端同口径：去掉 /__aisys__/api 前缀后比对。
      if (r.template === SYSTEM_API_PREFIX) return { ...r, template: '/' }
      assert.ok(r.template.startsWith(SYSTEM_API_PREFIX + '/'), `surface=${r.surface} 模板缺少前缀: ${r.template}`)
      return { ...r, template: r.template.slice(SYSTEM_API_PREFIX.length) }
    })
  const goIndex = new Map<string, GoRoute[]>()
  for (const route of assertable) {
    const key = `${route.method} ${normalizeGoTemplateForMatch(route.template)}`
    const list = goIndex.get(key) ?? []
    list.push(route)
    goIndex.set(key, list)
  }

  let allowlist: AllowlistEntry[] = []
  if (existsSync(ALLOWLIST_PATH)) {
    const parsed = JSON.parse(readFileSync(ALLOWLIST_PATH, 'utf8')) as unknown
    assert.ok(Array.isArray(parsed), 'api-contract-allowlist.json 必须是数组')
    allowlist = parsed as AllowlistEntry[]
  } else {
    assert.fail(`白名单文件缺失: ${rel(ALLOWLIST_PATH)}`)
  }

  const missing: FrontendEndpoint[] = []
  const warned: FrontendEndpoint[] = []
  for (const endpoint of frontend) {
    const key = `${endpoint.method} ${endpoint.template}`
    if (goIndex.has(key)) continue
    const subtreeMatch = assertable.some(
      (r) => (r.method === endpoint.method || r.method === 'ANY') && templateMatches(endpoint.template, r.template)
    )
    if (subtreeMatch) continue
    const allowed = allowlist.find((entry) => entry.template === key)
    if (allowed) warned.push(endpoint)
    else missing.push(endpoint)
  }

  // 信息输出：/__aisys__/api 面中前端未调用的 Go 端点。
  const coveredTemplates = new Set<string>()
  for (const endpoint of frontend) {
    for (const route of assertable) {
      if ((route.method === endpoint.method || route.method === 'ANY') && templateMatches(endpoint.template, route.template)) {
        coveredTemplates.add(`${route.method} ${normalizeGoTemplateForMatch(route.template)}`)
      }
    }
  }
  const uncoveredSeen = new Set<string>()
  const uncoveredGo = assertable.filter((r) => {
    const key = `${r.method} ${normalizeGoTemplateForMatch(r.template)}`
    if (coveredTemplates.has(key) || uncoveredSeen.has(key)) return false
    uncoveredSeen.add(key)
    return true
  })

  const infoCounts = new Map<string, number>()
  for (const route of [...goRoutes, ...j3b.routes]) {
    if (route.surface === 'aisys' || route.surface === 'j3b') continue
    infoCounts.set(route.surface, (infoCounts.get(route.surface) ?? 0) + 1)
  }

  const infoLines: string[] = []
  for (const [surface, count] of [...infoCounts.entries()].sort()) {
    infoLines.push(`  - ${surface}: ${count} 条注册（前端不直接调用，仅计数）`)
  }
  if (uncoveredGo.length > 0) {
    infoLines.push(`  - /__aisys__/api 前端未调用端点 ${uncoveredGo.length} 条:`)
    for (const route of uncoveredGo.sort((a, b) => a.template.localeCompare(b.template))) {
      infoLines.push(`      ${route.method} ${normalizeGoTemplateForMatch(route.template)}  (${route.origins[0]})`)
    }
  }

  if (missing.length > 0) {
    const blocks = missing.map((endpoint) => {
      const key = `${endpoint.method} ${endpoint.template}`
      const sameTemplateDiffMethod = [...new Set(
        assertable
          .filter((r) => templateMatches(endpoint.template, r.template))
          .map((r) => `${r.method} ${normalizeGoTemplateForMatch(r.template)} (${r.origins[0]})`)
      )]
      const nearest = [...assertable]
        .filter((r) => r.method === endpoint.method)
        .map((r) => ({ normalized: normalizeGoTemplateForMatch(r.template), d: levenshtein(normalizeGoTemplateForMatch(r.template), endpoint.template), origin: r.origins[0] }))
        .sort((a, b) => a.d - b.d)
        .slice(0, 3)
        .map((c) => `${c.normalized} (编辑距离 ${c.d}, ${c.origin})`)
      return [
        `  ✗ ${key}`,
        `    前端调用点: ${endpoint.origins.join(', ')}`,
        ...(sameTemplateDiffMethod.length > 0
          ? [`    同路径不同方法的 Go 注册: ${sameTemplateDiffMethod.join(' | ')}`]
          : []),
        ...(nearest.length > 0 ? [`    最近 Go 候选: ${nearest.join(' | ')}`] : [])
      ].join('\n')
    })
    assert.fail([
      `前端调用了 ${frontend.length} 个端点模板，其中 ${missing.length} 个在 Go gateway 注册面（主 kernel + J3b）找不到匹配：`,
      ...blocks,
      '',
      '如属提取器缺陷请修复提取器；如属真实不对齐请交由主代理裁决；确认合理豁免可加入 api-contract-allowlist.json。'
    ].join('\n'))
  }

  for (const endpoint of warned) {
    const entry = allowlist.find((a) => a.template === `${endpoint.method} ${endpoint.template}`)
    console.log(`WARN 白名单豁免: ${endpoint.method} ${endpoint.template}（${entry?.reason ?? '无 reason'}）调用点 ${endpoint.origins.join(', ')}`)
  }
  const hitKeys = new Set(warned.map((w) => `${w.method} ${w.template}`))
  for (const entry of allowlist) {
    if (!hitKeys.has(entry.template)) {
      console.log(`WARN 白名单条目当前未命中差集（可考虑清理）: ${entry.template}`)
    }
  }

  const infoTotal = [...infoCounts.values()].reduce((a, b) => a + b, 0)
  console.log(`API 契约比对通过：前端 ${frontend.length} 个端点模板全部命中 Go 注册面（主 kernel + J3b model-checks）；Go 侧另有前端未调用端点 ${uncoveredGo.length} 条、信息面注册 ${infoTotal} 条（明细见上）。`)
  for (const line of infoLines) console.log(line)
}

main()

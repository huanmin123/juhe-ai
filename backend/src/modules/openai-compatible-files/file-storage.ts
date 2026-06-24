import { randomUUID } from 'node:crypto'
import { existsSync, mkdirSync } from 'node:fs'
import { unlink } from 'node:fs/promises'
import { dirname, relative, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

export const openAICompatibleFileMaxBytes = 512 * 1024 * 1024
export const openAICompatibleBridgeFileMaxBytes = 32 * 1024 * 1024

export function newOpenAICompatibleFileId(): string {
  return `file-${Date.now().toString(36)}-${randomUUID().replace(/-/g, '').slice(0, 20)}`
}

export function storageKeyForOpenAICompatibleFile(fileId: string): string {
  const safeId = safeStorageSegment(fileId)
  const shard = safeId.slice(0, 8) || 'default'
  return `files/${shard}/${safeId}`
}

export function openAICompatibleFileObjectPath(storageKey: string): string {
  const root = openAICompatibleFilesRoot()
  const target = resolve(root, storageKey)
  const relativePath = relative(root, target)
  if (relativePath.startsWith('..') || relativePath === '' || /^[A-Za-z]:/.test(relativePath)) {
    throw new Error('OpenAI compatible file storage key escaped storage root')
  }
  return target
}

export function ensureOpenAICompatibleFileObjectParent(storageKey: string): string {
  const filePath = openAICompatibleFileObjectPath(storageKey)
  mkdirSync(dirname(filePath), { recursive: true })
  return filePath
}

export async function removeOpenAICompatibleFileObject(storageKey: string): Promise<void> {
  const filePath = openAICompatibleFileObjectPath(storageKey)
  if (!existsSync(filePath)) return
  await unlink(filePath)
}

export function mediaTypeFromFilename(filename: string | undefined): string | undefined {
  const lower = filename?.trim().toLowerCase()
  if (!lower) return undefined
  if (lower.endsWith('.pdf')) return 'application/pdf'
  if (lower.endsWith('.txt')) return 'text/plain'
  if (lower.endsWith('.md') || lower.endsWith('.markdown')) return 'text/markdown'
  if (lower.endsWith('.csv')) return 'text/csv'
  if (lower.endsWith('.json')) return 'application/json'
  if (lower.endsWith('.c')) return 'text/x-c'
  if (lower.endsWith('.cpp') || lower.endsWith('.cc') || lower.endsWith('.cxx')) return 'text/x-c++'
  if (lower.endsWith('.cs')) return 'text/x-csharp'
  if (lower.endsWith('.css')) return 'text/css'
  if (lower.endsWith('.go')) return 'text/x-golang'
  if (lower.endsWith('.html') || lower.endsWith('.htm')) return 'text/html'
  if (lower.endsWith('.java')) return 'text/x-java'
  if (lower.endsWith('.js') || lower.endsWith('.mjs') || lower.endsWith('.cjs')) return 'text/javascript'
  if (lower.endsWith('.php')) return 'text/x-php'
  if (lower.endsWith('.py')) return 'text/x-python'
  if (lower.endsWith('.rb')) return 'text/x-ruby'
  if (lower.endsWith('.tex')) return 'text/x-tex'
  if (lower.endsWith('.ts') || lower.endsWith('.tsx')) return 'application/typescript'
  if (lower.endsWith('.sh')) return 'application/x-sh'
  if (lower.endsWith('.png')) return 'image/png'
  if (lower.endsWith('.jpg') || lower.endsWith('.jpeg')) return 'image/jpeg'
  if (lower.endsWith('.gif')) return 'image/gif'
  if (lower.endsWith('.webp')) return 'image/webp'
  return undefined
}

export function normalizeOpenAICompatibleFileMediaType(value: string | undefined, filename: string | undefined): string | undefined {
  const mediaType = value?.split(';', 1)[0]?.trim().toLowerCase()
  if (mediaType && mediaType !== 'application/octet-stream') return mediaType
  return mediaTypeFromFilename(filename)
}

function openAICompatibleFilesRoot(): string {
  const root = resolve(runtimeConfig.openAICompatibleFilesRoot)
  mkdirSync(root, { recursive: true })
  return root
}

function safeStorageSegment(value: string): string {
  return value.replace(/[^A-Za-z0-9_.-]/g, '_').slice(0, 160) || 'file'
}

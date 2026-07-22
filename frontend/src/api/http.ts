import axios from 'axios'

import { extractResponseErrorMessage, localizeTransportErrorMessage } from '@/shared/apiError'

interface ApiResponse<T> {
  data: T
  message?: string
}

const viteApiBaseUrl = (import.meta.env as { VITE_JUHE_AI_API_BASE_URL?: string } | undefined)?.VITE_JUHE_AI_API_BASE_URL

export const http = axios.create({
  baseURL: normalizeApiBaseUrl(viteApiBaseUrl),
  timeout: 15000,
  withCredentials: true
})

let unauthorizedHandler: (() => void) | undefined
let mustChangePasswordHandler: (() => void) | undefined

export function setUnauthorizedHandler(handler: () => void): void {
  unauthorizedHandler = handler
}

export function setMustChangePasswordHandler(handler: () => void): void {
  mustChangePasswordHandler = handler
}

http.interceptors.response.use((response) => response, (error: unknown) => {
  if (axios.isAxiosError(error) && error.response?.status === 401 && shouldNotifyUnauthorized(error.config?.url)) {
    unauthorizedHandler?.()
  } else if (axios.isAxiosError(error) && error.response?.status === 403 && isMustChangePasswordResponse(error.response.data)) {
    mustChangePasswordHandler?.()
  }
  return Promise.reject(error)
})

export function normalizeApiBaseUrl(value?: string): string {
  const text = value?.trim()
  if (!text) return '/__aisys__/api'
  return text.replace(/\/+$/, '') || '/__aisys__/api'
}

function shouldNotifyUnauthorized(url?: string): boolean {
  if (!url) return true
  return !url.startsWith('/auth/')
}

function isMustChangePasswordResponse(data: unknown): boolean {
  return typeof data === 'object'
    && data !== null
    && !Array.isArray(data)
    && (data as { code?: unknown }).code === 'must_change_password'
}

export async function unwrap<T>(request: Promise<{ data: ApiResponse<T> }>): Promise<T> {
  const response = await request
  return response.data.data
}

export const noTimeout = { timeout: 0 }

export function apiUrl(path: string, params?: Record<string, string | undefined>): string {
  const baseUrl = http.defaults.baseURL ?? '/__aisys__/api'
  const normalizedPath = path.startsWith('/') ? path : `/${path}`
  const url = new URL(`${baseUrl}${normalizedPath}`, window.location.origin)
  Object.entries(params ?? {}).forEach(([key, value]) => {
    if (value) url.searchParams.set(key, value)
  })
  return url.toString()
}

export async function readFetchErrorMessage(response: Response, path?: string): Promise<string> {
  const text = await response.text()
  notifyAuthFailure(response.status, text, path)
  if (!text.trim()) return `请求失败：HTTP ${response.status}`
  try {
    return extractResponseErrorMessage(JSON.parse(text) as unknown) ?? text
  } catch {
    return localizeTransportErrorMessage(text, `请求失败：HTTP ${response.status}`)
  }
}

function notifyAuthFailure(status: number, responseText: string, path?: string): void {
  if (status === 401 && shouldNotifyUnauthorized(path)) {
    unauthorizedHandler?.()
    return
  }
  if (status === 403 && isMustChangePasswordResponse(parseJsonPayload(responseText))) {
    mustChangePasswordHandler?.()
  }
}

function parseJsonPayload(text: string): unknown {
  try {
    return JSON.parse(text) as unknown
  } catch {
    return { message: text }
  }
}

export function queryString(params?: object): string {
  if (!params) return ''
  const searchParams = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue
    searchParams.set(key, String(value))
  }
  const text = searchParams.toString()
  return text ? `?${text}` : ''
}

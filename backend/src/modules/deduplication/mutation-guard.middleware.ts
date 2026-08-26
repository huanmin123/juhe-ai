import type { Request, RequestHandler } from 'express'

import { badRequest } from '../../shared/http.js'
import { getRequestAuthContext } from '../auth/request-context.js'
import { hashStableValue } from './deduplication.service.js'
import { operationDeduplicationService } from './deduplication.service.js'

export interface MutationGuardConfig {
  operationKey: string
  fingerprint: (req: Request) => unknown
  scope?: (req: Request) => string | undefined
  processingTtlMs?: number
  succeededTtlMs?: number
  failedTtlMs?: number
}

export function mutationGuard(config: MutationGuardConfig): RequestHandler {
  return (req, res, next) => {
    if (req.method === 'GET' || req.method === 'HEAD' || req.method === 'OPTIONS') {
      next()
      return
    }

    const actor = getRequestAuthContext()?.systemAccountId ?? 'anonymous'
    let fingerprint: unknown
    let operationScope: string | undefined
    try {
      fingerprint = config.fingerprint(req)
      operationScope = config.scope?.(req)
    } catch (error) {
      res.status(400).json(badRequest(error instanceof Error ? error.message : '请求参数无效'))
      return
    }

    const key = [
      actor,
      operationScope ?? '',
      req.method.toUpperCase(),
      config.operationKey,
      hashStableValue(fingerprint)
    ].join(':')
    const claim = operationDeduplicationService.claim({
      key,
      operationKey: config.operationKey,
      processingTtlMs: config.processingTtlMs
    })

    if (!claim.claimed) {
      res.status(409).json({
        message: duplicateMessage(claim.entry.status)
      })
      return
    }

    let completed = false
    const complete = (status: 'succeeded' | 'failed') => {
      if (completed) return
      completed = true
      operationDeduplicationService.complete({
        key,
        status,
        succeededTtlMs: config.succeededTtlMs,
        failedTtlMs: config.failedTtlMs
      })
    }

    res.once('finish', () => {
      complete(res.statusCode >= 200 && res.statusCode < 400 ? 'succeeded' : 'failed')
    })
    res.once('close', () => {
      if (!res.writableEnded) {
        complete('failed')
      }
    })

    next()
  }
}

export function bodyField(req: Request, name: string): unknown {
  return isRecord(req.body) ? req.body[name] : undefined
}

export function queryField(req: Request, name: string): unknown {
  const value = req.query[name]
  return Array.isArray(value) ? value[0] : value
}

export function normalizedText(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

export function textValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

export function sortedTextValues(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return []
  }
  return value
    .filter((item): item is string => typeof item === 'string')
    .map((item) => item.trim())
    .filter(Boolean)
    .sort()
}

export function sensitiveFingerprint(value: unknown): string {
  return typeof value === 'string' && value.trim() ? hashStableValue(value.trim()) : ''
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function duplicateMessage(status: 'processing' | 'succeeded' | 'failed'): string {
  if (status === 'processing') {
    return '请求正在处理中，请勿重复提交'
  }
  if (status === 'failed') {
    return '请求刚刚失败，请稍后重试'
  }
  return '该操作刚刚已处理，请刷新列表查看结果'
}

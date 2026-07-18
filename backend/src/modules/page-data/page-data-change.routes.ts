import { Router } from 'express'

import { isAdminRole } from '../../domain/types.js'
import { ok } from '../../shared/http.js'
import { getRequestAuthContext } from '../auth/request-context.js'
import {
  PAGE_DATA_MAX_CONFIRM_DOMAINS,
  pageDataDomains,
  pageDataScope,
  type PageDataChangeStore,
  type PageDataRevisionToken,
  type PageDataViewScope
} from './page-data-change.service.js'

export function createPageDataChangesRouter(options: { store: PageDataChangeStore }): Router {
  const router = Router()

  router.post('/confirm', async (req, res) => {
    const context = getRequestAuthContext()
    if (!context) {
      res.status(401).json({ message: '请先登录' })
      return
    }
    let input: ReturnType<typeof parseConfirmRequest>
    try {
      input = parseConfirmRequest(req.body)
    } catch (error) {
      res.status(400).json({ message: error instanceof Error ? error.message : '变更确认请求无效' })
      return
    }
    if (input.viewScope === 'admin' && !isAdminRole(context.role)) {
      res.status(403).json({ message: '需要管理员权限' })
      return
    }
    const scope = pageDataScope({
      viewerSystemAccountId: context.systemAccountId,
      viewScope: input.viewScope,
      targetSystemAccountId: input.targetSystemAccountId
    })
    try {
      res.json(ok(await options.store.confirm(scope, input.domains)))
    } catch (error) {
      res.setHeader('Retry-After', '5')
      res.status(503).json({ message: '页面数据变更确认暂不可用，请稍后重试' })
    }
  })

  return router
}

function parseConfirmRequest(value: unknown): {
  viewScope: PageDataViewScope
  targetSystemAccountId?: string
  domains: Record<string, PageDataRevisionToken | undefined>
} {
  const body = recordValue(value, '变更确认请求体')
  const viewScope = body.viewScope === 'admin' ? 'admin' : body.viewScope === 'self' ? 'self' : undefined
  if (!viewScope) throw new Error('viewScope 只能是 self 或 admin')
  const targetSystemAccountId = optionalString(body.targetSystemAccountId, 'targetSystemAccountId')
  if (viewScope === 'self' && targetSystemAccountId) {
    throw new Error('self 视图不能指定目标系统账户')
  }
  const rawDomains = recordValue(body.domains, 'domains')
  const entries = Object.entries(rawDomains)
  if (entries.length > PAGE_DATA_MAX_CONFIRM_DOMAINS) {
    throw new Error(`单次最多确认 ${PAGE_DATA_MAX_CONFIRM_DOMAINS} 个数据域`)
  }
  const domains: Record<string, PageDataRevisionToken | undefined> = {}
  for (const [domain, rawToken] of entries) {
    if (!(pageDataDomains as readonly string[]).includes(domain)) throw new Error(`不支持的数据域：${domain}`)
    domains[domain] = rawToken === null || rawToken === undefined
      ? undefined
      : parseRevisionToken(rawToken)
  }
  return { viewScope, ...(targetSystemAccountId ? { targetSystemAccountId } : {}), domains }
}

function parseRevisionToken(value: unknown): PageDataRevisionToken {
  const token = recordValue(value, 'revision token')
  const protocolVersion = integerValue(token.protocolVersion, 'protocolVersion')
  const epoch = requiredString(token.epoch, 'epoch')
  const scope = requiredString(token.scope, 'scope')
  const domain = requiredString(token.domain, 'domain') as PageDataRevisionToken['domain']
  const sequence = integerValue(token.sequence, 'sequence')
  const resetSequence = integerValue(token.resetSequence, 'resetSequence')
  if (sequence < 0) throw new Error('sequence 不能小于 0')
  if (resetSequence < 0) throw new Error('resetSequence 不能小于 0')
  return { protocolVersion, epoch, scope, domain, sequence, resetSequence }
}

function recordValue(value: unknown, label: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(`${label}必须是对象`)
  return value as Record<string, unknown>
}

function requiredString(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) throw new Error(`${label}不能为空`)
  return value.trim()
}

function optionalString(value: unknown, label: string): string | undefined {
  if (value === undefined || value === null || value === '') return undefined
  return requiredString(value, label)
}

function integerValue(value: unknown, label: string): number {
  if (!Number.isSafeInteger(value)) throw new Error(`${label}必须是整数`)
  return Number(value)
}

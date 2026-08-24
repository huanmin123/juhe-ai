import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText } from '../../shared/query-values.js'
import { rfc3339InstantSchema } from '../../shared/zod-rfc3339.js'
import { createProxyAsync, deleteProxyForManagementAsync, findProxyAsync, listProxiesPageAsync, listProxyOptionsAsync, patchProxyForManagementAsync, ProxyInUseError, ProxyProfileUpdateConflictError } from '../../storage/repositories.js'
import { getProxyTestConfigAsync } from '../../storage/proxy.repository.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { bodyField, mutationGuard, normalizedText, sensitiveFingerprint } from '../deduplication/mutation-guard.middleware.js'
import { diagnosticTaskBusyMessage, diagnosticTaskRetryAfterSeconds, tryAcquireDiagnosticTaskSlot } from '../diagnostics/diagnostic-task-limiter.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import { diffSafeFields, runLoggedOperationAsync, safeChange } from '../operation-logs/operation-log.service.js'
import { manualProxyTestDeadlineMs, testProxyById, type ProxyTestReport } from './proxy-test.service.js'
import { proxyLatencyGoHandoverReady, proxyLatencyGoOwnerEnabled, runProxyLatencyManualViaGo } from '../background/proxy-latency-handover.js'

export const proxiesRouter = Router()

const proxySchema = z.object({
  name: z.string().trim().min(1),
  description: z.string().trim().max(200).nullable().optional(),
  type: z.enum(['http', 'https', 'socks5', 'socks5h']),
  host: z.string().trim().min(1),
  port: z.number().int().min(1).max(65535),
  username: z.string().optional(),
  password: z.string().optional(),
  enabled: z.boolean().optional()
}).strict()

const proxyUpdateSchema = proxySchema.partial().extend({
  expectedUpdatedAt: rfc3339InstantSchema('代理配置版本无效')
}).strict().refine((value) => Object.keys(value).some((key) => key !== 'expectedUpdatedAt'), {
  message: '代理更新内容不能为空'
})

proxiesRouter.get('/options', async (req, res, next) => {
  try {
    res.json(ok(await listProxyOptionsAsync(parseProxyOptionListOptions(req.query as Record<string, unknown>))))
  } catch (error) {
    if (error instanceof ProxyOptionQueryError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    next(error)
  }
})

proxiesRouter.get('/', requireAdmin, async (req, res, next) => {
  try {
    res.json(ok(await listProxiesPageAsync(parseProxyListOptions(req.query))))
  } catch (error) {
    next(error)
  }
})

function parseProxyListOptions(query: Record<string, unknown>) {
  return {
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize),
    keyword: optionalQueryText(query.keyword)
  }
}

function parseProxyOptionListOptions(query: Record<string, unknown>) {
  return {
    keyword: optionalQueryText(query.keyword),
    limit: optionLimitValue(integerQueryValue(query.limit)),
    selectedIds: parseSelectedProxyOptionIds(query)
  }
}

class ProxyOptionQueryError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'ProxyOptionQueryError'
  }
}

function parseSelectedProxyOptionIds(query: Record<string, unknown>): string[] | undefined {
  for (const key of Object.keys(query)) {
    if (key === 'selectedIds' || key === 'selectedIds[]') continue
    if (key.startsWith('selectedIds[')) {
      throw new ProxyOptionQueryError('代理选项 selectedIds 无效')
    }
  }

  const rawValues: unknown[] = []
  if (Object.prototype.hasOwnProperty.call(query, 'selectedIds')) {
    rawValues.push(...normalizeSelectedProxyOptionRaw(query.selectedIds))
  }
  if (Object.prototype.hasOwnProperty.call(query, 'selectedIds[]')) {
    rawValues.push(...normalizeSelectedProxyOptionRaw(query['selectedIds[]']))
  }
  if (rawValues.length === 0) return undefined

  const selectedIds: string[] = []
  const seen = new Set<string>()
  for (const value of rawValues) {
    if (typeof value !== 'string') {
      throw new ProxyOptionQueryError('代理选项 selectedIds 无效')
    }
    const text = value.trim()
    if (!text) continue
    if (text.includes(',') || text.startsWith('[') || text.length > 120) {
      throw new ProxyOptionQueryError('代理选项 selectedIds 无效')
    }
    if (seen.has(text)) continue
    seen.add(text)
    selectedIds.push(text)
  }
  selectedIds.sort((left, right) => (left < right ? -1 : left > right ? 1 : 0))
  if (selectedIds.length > 20) {
    throw new ProxyOptionQueryError('代理选项 selectedIds 最多 20 个')
  }
  return selectedIds
}

function normalizeSelectedProxyOptionRaw(value: unknown): unknown[] {
  if (value === undefined || value === null) return []
  if (Array.isArray(value)) return value
  if (typeof value === 'object') {
    throw new ProxyOptionQueryError('代理选项 selectedIds 无效')
  }
  return [value]
}

function optionLimitValue(value: number | undefined): number {
  return typeof value === 'number' ? Math.min(50, Math.max(1, value)) : 50
}

proxiesRouter.post('/', requireAdmin, mutationGuard({
  operationKey: 'proxies.create',
  fingerprint: (req) => ({
    name: normalizedText(bodyField(req, 'name')),
    type: normalizedText(bodyField(req, 'type')),
    host: normalizedText(bodyField(req, 'host')),
    port: bodyField(req, 'port'),
    username: normalizedText(bodyField(req, 'username')),
    password: sensitiveFingerprint(bodyField(req, 'password'))
  })
}), async (req, res) => {
  const parsed = proxySchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('代理参数无效'))
    return
  }
  try {
    const requestAccess = getRequestAccessScope()
    if (!requestAccess) {
      res.status(401).json(badRequest('缺少系统账户上下文'))
      return
    }
    const proxy = await runLoggedOperationAsync(async () => {
      const proxy = await createProxyAsync(parsed.data, requestAccess)
      return {
        result: proxy,
        log: {
          mode: 'admin',
          module: 'proxies',
          action: 'create',
          operationKey: 'proxies.create',
          resourceType: 'proxy',
          resourceId: proxy.id,
          resourceName: proxy.name,
          summary: `创建代理：${proxy.name}`,
          visibilityScope: 'admin_only',
          changes: [
            safeChange('name', '名称', undefined, proxy.name),
            safeChange('type', '类型', undefined, proxy.type),
            safeChange('host', '主机', undefined, proxy.host),
            safeChange('port', '端口', undefined, proxy.port),
            safeChange('username', '用户名', undefined, proxy.username),
            safeChange('password', '密码', undefined, parsed.data.password),
            safeChange('enabled', '启用状态', undefined, proxy.enabled)
          ]
        }
      }
    }, req)
    res.status(201).json(ok(proxy))
  } catch (error) {
    const message = error instanceof Error ? error.message : '创建代理失败'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

proxiesRouter.patch('/:id', requireAdmin, async (req, res) => {
  try {
    const parsed = proxyUpdateSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest('代理参数无效'))
      return
    }
    const requestAccess = getRequestAccessScope()
    if (!requestAccess) {
      res.status(401).json(badRequest('缺少系统账户上下文'))
      return
    }
    const { expectedUpdatedAt, ...body } = parsed.data
    const outcome = await patchProxyForManagementAsync(
      req.params.id,
      body,
      expectedUpdatedAt
    )
    if (!outcome) {
      res.status(404).json({ message: '代理不存在' })
      return
    }
    if (!outcome.mutation.changed) {
      res.json(ok(outcome.mutation))
      return
    }
    const mutation = await runLoggedOperationAsync(async () => {
      return {
        result: outcome.mutation,
        log: {
          mode: 'admin',
          module: 'proxies',
          action: 'update',
          operationKey: 'proxies.update',
          resourceType: 'proxy',
          resourceId: outcome.mutation.id,
          resourceName: outcome.name,
          summary: `更新代理：${outcome.name}`,
          visibilityScope: 'admin_only',
          changes: [
            ...diffSafeFields(outcome.before, outcome.after, {
              name: '名称',
              description: '说明',
              type: '类型',
              host: '主机',
              port: '端口',
              username: '用户名',
              enabled: '启用状态'
            }),
            ...(outcome.passwordChanged
              ? [safeChange('password', '密码', undefined, body.password)]
              : [])
          ]
        }
      }
    }, req)
    res.json(ok(mutation))
  } catch (error) {
    if (error instanceof Error && error.message === '代理不存在') {
      res.status(404).json({ message: '代理不存在' })
      return
    }
    if (error instanceof ProxyProfileUpdateConflictError) {
      res.status(409).json({ message: error.message })
      return
    }
    const message = error instanceof Error ? error.message : '更新代理失败'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

proxiesRouter.post('/:id/test', requireAdmin, async (req, res) => {
  const before = await findProxyAsync(req.params.id)
  if (!before) {
    res.status(404).json({ message: '代理不存在' })
    return
  }
  const releaseDiagnosticSlot = tryAcquireDiagnosticTaskSlot()
  if (!releaseDiagnosticSlot) {
    res.setHeader('Retry-After', String(diagnosticTaskRetryAfterSeconds))
    res.status(503).json({ message: diagnosticTaskBusyMessage })
    return
  }
  try {
    const goOwner = proxyLatencyGoOwnerEnabled()
    if (goOwner && !proxyLatencyGoHandoverReady()) {
      throw new Error('J3a Go owner handoff gate 未完成')
    }
    const execution = goOwner
      ? await runGoProxyManualExecution(req.params.id)
      : await testProxyById(req.params.id, { persist: false, deadlineMs: manualProxyTestDeadlineMs })
    if (!execution) {
      res.status(404).json({ message: '代理不存在' })
      return
    }
    const { report } = execution
    const after = await requestDbService({
      type: 'update_proxy_test_state',
      proxyId: report.proxyId,
      input: {
        testStatus: report.status,
        latencyMs: report.baseLatencyMs,
        outboundIp: report.outboundIp,
        outboundRegion: report.outboundRegion,
        lastTestMessage: report.message,
        lastTestedAt: report.testedAt,
        expectedConfigUpdatedAt: execution.configUpdatedAt
      }
    })
    if (after.updated && after.proxyStatus !== report.status) {
      throw new Error('DB service 未确认代理检测状态写入成功')
    }
    await runLoggedOperationAsync(async () => {
      return {
        result: report,
        log: {
          mode: 'admin',
          module: 'proxies',
          action: 'test',
          operationKey: 'proxies.test',
          resourceType: 'proxy',
          resourceId: report.proxyId,
          resourceName: report.proxyName,
          summary: `检测代理：${report.proxyName}`,
          visibilityScope: 'admin_only',
          changes: diffSafeFields(before as unknown as Record<string, unknown> | undefined, {
            ...before,
            testStatus: report.status,
            latencyMs: report.baseLatencyMs,
            outboundIp: report.outboundIp,
            outboundRegion: report.outboundRegion,
            lastTestMessage: report.message,
            lastTestedAt: report.testedAt
          } as Record<string, unknown>, {
            testStatus: '检测状态',
            latencyMs: '延迟',
            outboundIp: '出口 IP',
            outboundRegion: '出口地区',
            lastTestMessage: '检测消息',
            lastTestedAt: '检测时间'
          })
        }
      }
    }, req)
    res.json(ok(report))
  } catch (error) {
    if (error instanceof Error && error.message === '代理不存在') {
      res.status(404).json({ message: '代理不存在' })
      return
    }
    res.status(502).json({ message: error instanceof Error ? error.message : '代理检测失败' })
  } finally {
    releaseDiagnosticSlot()
  }
})

async function runGoProxyManualExecution(proxyId: string): Promise<{ report: ProxyTestReport; configUpdatedAt: string } | undefined> {
  const config = await getProxyTestConfigAsync(proxyId)
  if (!config) return undefined
  const report = await runProxyLatencyManualViaGo(config, { timeoutMs: manualProxyTestDeadlineMs })
  return { report, configUpdatedAt: config.configUpdatedAt }
}

proxiesRouter.delete('/:id', requireAdmin, async (req, res) => {
  try {
    await runLoggedOperationAsync(async () => {
      const deleted = await deleteProxyForManagementAsync(req.params.id)
      if (!deleted) {
        throw new Error('代理不存在')
      }
      return {
        result: true,
        log: {
          mode: 'admin',
          module: 'proxies',
          action: 'delete',
          operationKey: 'proxies.delete',
          resourceType: 'proxy',
          resourceId: req.params.id,
          resourceName: deleted.name,
          summary: `删除代理：${deleted.name}`,
          visibilityScope: 'admin_only',
          changes: [safeChange('deleted', '删除状态', false, true)]
        }
      }
    }, req)
    res.status(204).send()
  } catch (error) {
    if (error instanceof Error && error.message === '代理不存在') {
      res.status(404).json({ message: '代理不存在' })
      return
    }
    if (error instanceof ProxyInUseError) {
      res.status(409).json({ message: error.message })
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : '删除代理失败'))
  }
})

import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText } from '../../shared/query-values.js'
import { createProxy, deleteProxy, findProxy, listProxiesPage, listProxyOptions, ProxyInUseError, updateProxy, updateProxyTestState } from '../../storage/repositories.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { bodyField, mutationGuard, normalizedText, sensitiveFingerprint } from '../deduplication/mutation-guard.middleware.js'
import { diagnosticTaskBusyMessage, diagnosticTaskRetryAfterSeconds, tryAcquireDiagnosticTaskSlot } from '../diagnostics/diagnostic-task-limiter.js'
import { diffSafeFields, runLoggedOperation, safeChange } from '../operation-logs/operation-log.service.js'
import { testProxyById } from './proxy-test.service.js'

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

const proxyUpdateSchema = proxySchema.partial().strict()

proxiesRouter.get('/options', (req, res) => {
  res.json(ok(listProxyOptions(parseProxyOptionListOptions(req.query))))
})

proxiesRouter.get('/', requireAdmin, (req, res) => {
  res.json(ok(listProxiesPage(parseProxyListOptions(req.query))))
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
    limit: optionLimitValue(integerQueryValue(query.limit))
  }
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
}), (req, res) => {
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
    const proxy = runLoggedOperation(() => {
      const proxy = createProxy(parsed.data, requestAccess)
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

proxiesRouter.patch('/:id', requireAdmin, (req, res) => {
  try {
    const parsed = proxyUpdateSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest('代理参数无效'))
      return
    }
    const body = parsed.data as Record<string, unknown>
    const proxy = runLoggedOperation(() => {
      const before = findProxy(req.params.id)
      const proxy = updateProxy(req.params.id, body)
      if (!proxy) {
        throw new Error('代理不存在')
      }
      return {
        result: proxy,
        log: {
          mode: 'admin',
          module: 'proxies',
          action: 'update',
          operationKey: 'proxies.update',
          resourceType: 'proxy',
          resourceId: proxy.id,
          resourceName: proxy.name,
          summary: `更新代理：${proxy.name}`,
          visibilityScope: 'admin_only',
          changes: [
            ...diffSafeFields(before as unknown as Record<string, unknown> | undefined, proxy as unknown as Record<string, unknown>, {
              name: '名称',
              description: '说明',
              type: '类型',
              host: '主机',
              port: '端口',
              username: '用户名',
              enabled: '启用状态'
            }),
            ...(typeof body.password === 'string' && String(body.password).trim()
              ? [safeChange('password', '密码', undefined, body.password)]
              : [])
          ]
        }
      }
    }, req)
    res.json(ok(proxy))
  } catch (error) {
    if (error instanceof Error && error.message === '代理不存在') {
      res.status(404).json({ message: '代理不存在' })
      return
    }
    const message = error instanceof Error ? error.message : '更新代理失败'
    res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
  }
})

proxiesRouter.post('/:id/test', requireAdmin, async (req, res) => {
  const before = findProxy(req.params.id)
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
    const report = await testProxyById(req.params.id, { persist: false })
    if (!report) {
      res.status(404).json({ message: '代理不存在' })
      return
    }
    runLoggedOperation(() => {
      const after = updateProxyTestState(report.proxyId, {
        testStatus: report.status,
        latencyMs: report.baseLatencyMs,
        outboundIp: report.outboundIp,
        outboundRegion: report.outboundRegion,
        lastTestMessage: report.message,
        lastTestedAt: report.testedAt
      })
      if (!after) {
        throw new Error('代理不存在')
      }
      return {
        result: after,
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
          changes: diffSafeFields(before as unknown as Record<string, unknown> | undefined, after as unknown as Record<string, unknown> | undefined, {
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

proxiesRouter.delete('/:id', requireAdmin, (req, res) => {
  try {
    runLoggedOperation(() => {
      const before = findProxy(req.params.id)
      if (!deleteProxy(req.params.id)) {
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
          resourceName: before?.name ?? req.params.id,
          summary: `删除代理：${before?.name ?? req.params.id}`,
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

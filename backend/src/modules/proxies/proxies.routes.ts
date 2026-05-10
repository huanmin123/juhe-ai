import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { createProxy, deleteProxy, listProxies, listProxyOptions, ProxyInUseError, updateProxy, updateProxyTestState } from '../../storage/repositories.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { bodyField, mutationGuard, normalizedText, sensitiveFingerprint } from '../deduplication/mutation-guard.middleware.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'
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
})

proxiesRouter.get('/options', (_req, res) => {
  res.json(ok(listProxyOptions()))
})

proxiesRouter.get('/', requireAdmin, (_req, res) => {
  res.json(ok(listProxies()))
})

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
    const proxy = runLoggedOperation(() => {
      const proxy = createProxy(parsed.data)
      return {
        result: proxy,
        afterCommit: clearGatewayRuntimeCache,
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
    const body = req.body as Record<string, unknown>
    const proxy = runLoggedOperation(() => {
      const before = listProxies().find((item) => item.id === req.params.id)
      const proxy = updateProxy(req.params.id, body)
      if (!proxy) {
        throw new Error('代理不存在')
      }
      return {
        result: proxy,
        afterCommit: clearGatewayRuntimeCache,
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
  try {
    const before = listProxies().find((item) => item.id === req.params.id)
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
  }
})

proxiesRouter.delete('/:id', requireAdmin, (req, res) => {
  try {
    runLoggedOperation(() => {
      const before = listProxies().find((item) => item.id === req.params.id)
      if (!deleteProxy(req.params.id)) {
        throw new Error('代理不存在')
      }
      return {
        result: true,
        afterCommit: clearGatewayRuntimeCache,
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

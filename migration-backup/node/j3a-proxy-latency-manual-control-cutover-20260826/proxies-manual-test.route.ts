// 从 backend/src/modules/proxies/proxies.routes.ts 删除的 J3a Node 手动测试路由。
// 归档仅用于追溯，禁止重新接回运行路径。
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
    const execution = await runGoProxyManualExecution(req.params.id)
    if (!execution) {
      res.status(404).json({ message: '代理不存在' })
      return
    }
    const after = await findProxyAsync(req.params.id)
    if (!after) {
      res.status(404).json({ message: '代理不存在' })
      return
    }
    const { report } = execution
    await runLoggedOperationAsync(async () => ({
      result: report,
      log: {
        mode: 'admin', module: 'proxies', action: 'test', operationKey: 'proxies.test', resourceType: 'proxy',
        resourceId: report.proxyId, resourceName: report.proxyName, summary: `检测代理：${report.proxyName}`,
        visibilityScope: 'admin_only',
        changes: diffSafeFields(before as unknown as Record<string, unknown> | undefined, {
          ...before, testStatus: report.status, latencyMs: report.baseLatencyMs, outboundIp: report.outboundIp,
          outboundRegion: report.outboundRegion, lastTestMessage: report.message, lastTestedAt: report.testedAt
        } as Record<string, unknown>, {
          testStatus: '检测状态', latencyMs: '延迟', outboundIp: '出口 IP', outboundRegion: '出口地区',
          lastTestMessage: '检测消息', lastTestedAt: '检测时间'
        })
      }
    }), req)
    res.json(ok(report))
  } catch (error) {
    if (error instanceof GoManualBridgeHttpError) {
      if (error.status === 503) {
        if (error.retryAfter) res.setHeader('Retry-After', error.retryAfter)
        res.status(503).json({ message: '代理检测暂时繁忙' })
        return
      }
      res.status(502).json({ message: error.message })
      return
    }
    if (error instanceof Error && error.message === '代理不存在') {
      res.status(404).json({ message: '代理不存在' })
      return
    }
    res.status(502).json({ message: error instanceof Error ? error.message : '代理检测失败' })
  } finally {
    releaseDiagnosticSlot()
  }
})

async function runGoProxyManualExecution(proxyId: string): Promise<{ report: ProxyTestReport } | undefined> {
  const config = await getProxyTestConfigAsync(proxyId)
  if (!config) return undefined
  const report = await runProxyLatencyManualViaGo(config, { timeoutMs: manualProxyTestDeadlineMs })
  return { report }
}

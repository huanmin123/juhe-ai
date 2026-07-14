import { GPT_VENDOR_CODE, OPENAI_PROTOCOL_CODE } from '../../../../domain/provider-protocol.js'
import { getBusinessDatabase, nowIso } from '../../../../storage/database.js'
import {
  createExternalIntegrationSourceAuthorization,
  createExternalIntegrationSourceToken,
  externalIntegrationScopeOptions
} from '../../../../storage/external-integration-source.repository.js'
import { upsertCustomProviderModel } from '../../../../storage/custom-provider-models.repository.js'
import { createAnnouncement, markPublicAnnouncementsRead, upsertAccountUsageSnapshots } from '../../../../storage/repositories.js'
import { createResponseInspectionPolicy } from '../../../../storage/response-inspection-policy.repository.js'
import { dayMs, namePrefix, type MockAccounts, type MockExternalSources, type MockSystemAccounts } from '../shared.js'

export function createCustomProviderModels(adminId: string, users: MockSystemAccounts): number {
  const models = [
    {
      providerCode: GPT_VENDOR_CODE,
      model: 'mockdata-global-long-context',
      scope: 'global' as const,
      status: 'active' as const,
      mode: 'text',
      supportedApiProtocols: ['responses', 'chat_completions'],
      contextWindowTokens: 512_000,
      maxOutputTokens: 32_000,
      inputUsdPer1M: 0.2,
      outputUsdPer1M: 0.8,
      cachedInputUsdPer1M: 0.05,
      capabilityNotes: 'Mockdata 全局长上下文模型，用于模型目录、账户支持模型和映射目标展示',
      notes: 'Mockdata 全局模型样本',
      actorSystemAccountId: adminId
    },
    {
      providerCode: GPT_VENDOR_CODE,
      model: 'mockdata-global-image',
      scope: 'global' as const,
      status: 'active' as const,
      mode: 'image_generation',
      supportedApiProtocols: ['images', 'responses'],
      contextWindowTokens: 32_000,
      maxOutputTokens: 8_000,
      imageInputUsdPer1M: 5,
      imageOutputUsdPer1M: 40,
      outputUsdPerImage: 0.02,
      capabilityNotes: 'Mockdata 全局图像模型，用于图片用量与图像权限验收',
      notes: 'Mockdata 全局图像模型样本',
      actorSystemAccountId: adminId
    },
    {
      providerCode: GPT_VENDOR_CODE,
      model: 'mockdata-personal-codex',
      scope: 'personal' as const,
      systemAccountId: users.manager.id,
      status: 'active' as const,
      mode: 'text',
      supportedApiProtocols: ['responses'],
      contextWindowTokens: 256_000,
      maxOutputTokens: 16_000,
      inputUsdPer1M: 0.3,
      outputUsdPer1M: 1.2,
      capabilityNotes: 'Mockdata 普通管理员个人模型，用于个人模型目录和账户模型限制展示',
      notes: 'Mockdata 个人模型样本',
      actorSystemAccountId: users.manager.id
    },
    {
      providerCode: GPT_VENDOR_CODE,
      model: 'mockdata-draft-audio',
      scope: 'personal' as const,
      systemAccountId: adminId,
      status: 'draft' as const,
      mode: 'audio',
      supportedApiProtocols: ['audio', 'responses'],
      audioInputUsdPer1M: 1.5,
      audioOutputUsdPer1M: 6,
      capabilityNotes: 'Mockdata 草稿音频模型，用于模型目录草稿状态展示',
      notes: 'Mockdata 草稿模型样本',
      actorSystemAccountId: adminId
    },
    {
      providerCode: GPT_VENDOR_CODE,
      model: 'mockdata-disabled-legacy',
      scope: 'personal' as const,
      systemAccountId: adminId,
      status: 'disabled' as const,
      mode: 'text',
      supportedApiProtocols: ['chat_completions'],
      inputUsdPer1M: 0.1,
      outputUsdPer1M: 0.4,
      shutdownDate: new Date(Date.now() - 7 * dayMs).toISOString().slice(0, 10),
      capabilityNotes: 'Mockdata 停用模型，用于模型目录停用状态展示',
      notes: 'Mockdata 停用模型样本',
      actorSystemAccountId: adminId
    }
  ]

  for (const model of models) {
    upsertCustomProviderModel(model)
  }
  return models.length
}

export function createExternalSources(): MockExternalSources {
  const allScopes = externalIntegrationScopeOptions.map((option) => option.value)
  const readScopes = allScopes.filter((scope) => scope.includes(':read'))
  const primary = createExternalIntegrationSourceAuthorization({
    name: `${namePrefix}公益站公开接口`,
    status: 'active',
    scopes: allScopes,
    rateLimits: [
      { windowSeconds: 60, maxRequests: 180 },
      { windowSeconds: 3600, maxRequests: 6000 }
    ],
    expiresAt: new Date(Date.now() + 90 * dayMs).toISOString(),
    notes: 'Mockdata 正式来源系统，用于公开接口日志、鉴权和写接口演示'
  })
  createExternalIntegrationSourceToken({
    sourceRefId: primary.source.id,
    name: `${namePrefix}公益站备用 Token`,
    status: 'disabled',
    scopes: readScopes,
    expiresAt: new Date(Date.now() + 45 * dayMs).toISOString()
  })

  const readonly = createExternalIntegrationSourceAuthorization({
    name: `${namePrefix}只读统计来源`,
    status: 'active',
    scopes: readScopes,
    rateLimits: [
      { windowSeconds: 60, maxRequests: 90 },
      { windowSeconds: 3600, maxRequests: 2400 }
    ],
    notes: 'Mockdata 只读来源系统，用于公开统计读取接口演示'
  })
  return {
    primary,
    readonly
  }
}

export function createResponseInspectionPolicies(): number {
  const policies = [
    {
      name: `${namePrefix}响应错误切换账户`,
      enabled: true,
      priority: 20,
      scopeType: 'provider' as const,
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: GPT_VENDOR_CODE,
      match: {
        errorCodes: ['rate_limit_exceeded', 'server_error'],
        outputTextIncludes: ['Mockdata']
      },
      action: 'retry_next_account' as const,
      notes: 'Mockdata 管理端策略：命中响应错误后请求下一个账号'
    },
    {
      name: `${namePrefix}安全策略干跑观察`,
      enabled: true,
      priority: 35,
      scopeType: 'provider' as const,
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: GPT_VENDOR_CODE,
      match: {
        errorCodes: ['cyber_policy'],
        jsonPathsExists: ['response.error'],
        outputTextIncludes: ['policy']
      },
      action: 'observe' as const,
      notes: 'Mockdata GPT 供应商层策略：只观察安全策略命中，不改变响应'
    },
    {
      name: `${namePrefix}图像响应异常账号避让`,
      enabled: false,
      priority: 55,
      scopeType: 'provider' as const,
      protocolCode: OPENAI_PROTOCOL_CODE,
      providerCode: GPT_VENDOR_CODE,
      match: {
        finishReasons: ['failed'],
        outputTextIncludes: ['image_generation'],
        outputTextExcludes: ['completed']
      },
      action: 'avoid_account_ttl' as const,
      notes: 'Mockdata 停用策略，用于响应检查策略页面状态展示'
    }
  ]
  for (const policy of policies) {
    createResponseInspectionPolicy(policy)
  }
  return policies.length
}

export function createAnnouncements(adminId: string, users: MockSystemAccounts): void {
  const announcements = [
    createAnnouncement({
      title: `${namePrefix}系统维护公告`,
      content: '今晚 23:30 到 23:45 将进行 Mockdata 演示维护，期间可能出现短暂网关重试。',
      level: 'critical',
      status: 'published'
    }, adminId),
    createAnnouncement({
      title: `${namePrefix}额度观察提醒`,
      content: '主力分组本月额度接近 70%，请关注 API Key 额度窗口和授权用量。',
      level: 'warning',
      status: 'published'
    }, adminId),
    createAnnouncement({
      title: `${namePrefix}新模型接入说明`,
      content: 'Mockdata 已补充 gpt-5.4-mini、gpt-5.4 和 gpt-4.1-mini 的混合调用记录。',
      level: 'info',
      status: 'published'
    }, adminId),
    createAnnouncement({
      title: `${namePrefix}草稿公告`,
      content: '这是一条 Mockdata 草稿公告，用于公告管理页面状态展示。',
      level: 'normal',
      status: 'draft'
    }, adminId),
    createAnnouncement({
      title: `${namePrefix}归档公告`,
      content: '这是一条 Mockdata 归档公告，用于公告归档状态展示。',
      level: 'info',
      status: 'archived'
    }, adminId)
  ]
  const database = getBusinessDatabase()
  announcements.forEach((announcement, index) => {
    const createdAt = new Date(Date.now() - (20 - index * 3) * dayMs).toISOString()
    database.prepare(`
      UPDATE announcements
      SET created_at = ?, updated_at = ?, published_at = CASE WHEN status = 'published' THEN ? ELSE published_at END
      WHERE id = ?
    `).run(createdAt, createdAt, createdAt, announcement.id)
  })
  markPublicAnnouncementsRead(users.dev.id, [announcements[0].id, announcements[1].id])
  markPublicAnnouncementsRead(users.ops.id, [announcements[0].id])
  markPublicAnnouncementsRead(users.viewer.id, [announcements[0].id, announcements[1].id, announcements[2].id])
}

export function seedOauthUsageSnapshots(accounts: MockAccounts): void {
  const now = nowIso()
  upsertAccountUsageSnapshots([
    {
      accountId: accounts.oauth.id,
      kind: 'openai_codex',
      source: 'mockdata',
      snapshot: {
        codex_5h_used_percent: 62,
        codex_5h_reset_at: new Date(Date.now() + 2 * 60 * 60_000).toISOString(),
        codex_5h_window_minutes: 300,
        codex_7d_used_percent: 41,
        codex_7d_reset_at: new Date(Date.now() + 4 * dayMs).toISOString(),
        codex_7d_window_minutes: 10080
      },
      updatedAt: now
    },
    {
      accountId: accounts.oauthBackup.id,
      kind: 'openai_codex',
      source: 'mockdata',
      snapshot: {
        codex_5h_used_percent: 18,
        codex_5h_reset_at: new Date(Date.now() + 4 * 60 * 60_000).toISOString(),
        codex_5h_window_minutes: 300,
        codex_7d_used_percent: 9,
        codex_7d_reset_at: new Date(Date.now() + 6 * dayMs).toISOString(),
        codex_7d_window_minutes: 10080
      },
      updatedAt: now
    }
  ])
}

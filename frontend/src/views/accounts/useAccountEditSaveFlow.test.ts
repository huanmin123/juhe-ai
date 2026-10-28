import { beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, reactive, ref } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { defaultAccountForm } from './accountFormDefaults'
import type { AccountFormModel } from './accountFormTypes'
import { FALLBACK_PROVIDERS } from './accountOptions'
import {
  accountCreatedSuccessText,
  managedOAuthCreatedAccountText,
  useAccountEditSaveFlow
} from './useAccountEditSaveFlow'

vi.mock('@/lib/antd', () => ({
  message: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() }
}))

describe('accountCreatedSuccessText', () => {
  it('账户启用时返回基础文案', () => {
    expect(accountCreatedSuccessText('账户', true)).toBe('账户已创建并启用')
  })

  it('等待后台检查时返回基础文案', () => {
    expect(accountCreatedSuccessText('OAuth 账户', false)).toBe('OAuth 账户已创建，等待后台检查')
  })
})

describe('managedOAuthCreatedAccountText（方案 B：创建成功转编辑）', () => {
  it('转编辑文案表达自动派生与可继续调整语义', () => {
    expect(managedOAuthCreatedAccountText()).toBe('账户已创建，支持模型与检查项已自动配置，可继续调整')
  })
})

// ---- 方案 B 转编辑谓词：托管 OAuth 创建成功后的分域行为（B1） ----
// gemini google_oauth 是唯一同时出现在 chat 域与媒体域的托管创建入口：
// 媒体域经 AccountMediaForm 复用同一 saveAccount，转编辑必须只在 chat 域生效。

function mountedOAuthCreateFlow(capabilityDomain: AccountFormModel['capabilityDomain']) {
  const reopenCreatedAccountForEdit = vi.fn(async () => {})
  const loadData = vi.fn(async () => {})
  const form = reactive(defaultAccountForm('gemini', 'google_oauth', FALLBACK_PROVIDERS))
  form.capabilityDomain = capabilityDomain
  form.oauthType = 'code_assist'
  form.groupId = 'group-oauth-create'
  form.group = { id: form.groupId, name: 'OAuth 创建分组' }
  form.baseUrl = 'https://generativelanguage.googleapis.com/v1beta'
  form.proxyProfileId = 'proxy-oauth-create'
  form.callbackUrl = 'fake-auth-code'
  form.supportedModels = ['gemini-2.5-pro']
  form.healthCheckModel = 'gemini-2.5-pro'
  form.supportedEndpointModes = ['generate_content_json', 'generate_content_sse']
  form.healthCheckEndpointMode = 'generate_content_sse'
  // 创建提交发生在已打开的创建向导弹窗内：modalOpen 初始即 true，
  // 「转编辑保持 true / 旧路径显式置 false」才是可区分的真实断言。
  const modalOpen = ref(true)
  const ctx = useAccountEditSaveFlow({
    accountAdvancedDetailLoaded: ref(false),
    accountErrorPolicyRules: ref([]),
    accountResponseInspectionRules: ref([]),
    accounts: ref([]),
    createScopeParams: computed(() => undefined),
    cloningSourceId: ref(undefined),
    editingAccountDetail: ref(undefined),
    editingAccountAdvancedDetail: ref(undefined),
    editingAdvancedBaseline: ref(undefined),
    editingBasicBaseline: ref(undefined),
    editingAccountScopeParams: () => undefined,
    editingAuthorizedAccount: computed(() => false),
    editingId: ref(undefined),
    extractApiErrorMessage: (_error, fallback) => fallback,
    form,
    isManagementView: computed(() => false),
    loadData,
    refreshAccountMutationRows: vi.fn(async () => {}),
    mappingAnthropicSourceModelOptions: ref([]),
    mappingCurrentProviderSourceModelOptions: ref([]),
    mappingGeminiSourceModelOptions: ref([]),
    mappingSourceModelOptions: ref([]),
    providerModelOptions: ref([]),
    modalOpen,
    providers: ref(FALLBACK_PROVIDERS),
    reopenCreatedAccountForEdit
  })
  ctx.authResult.value = { authUrl: 'https://auth.example', sessionId: 'session-1' }
  return { ctx, modalOpen, loadData, reopenCreatedAccountForEdit }
}

describe('useAccountEditSaveFlow 托管 OAuth 创建成功（B1：转编辑仅限 chat 域）', () => {
  beforeEach(() => {
    vi.mocked(message.success).mockClear()
  })

  it('chat 域：先 loadData 后以新账户 id 转编辑，弹窗保持打开并提示转编辑文案', async () => {
    const createSpy = vi.spyOn(api.myGeminiOAuth, 'createFromCode')
      .mockResolvedValue({ id: 'acc-chat-create', status: 'active' })
    const flow = mountedOAuthCreateFlow('chat')
    try {
      await flow.ctx.saveAccount()
      expect(createSpy).toHaveBeenCalledTimes(1)
      expect(flow.loadData).toHaveBeenCalledTimes(1)
      expect(flow.reopenCreatedAccountForEdit).toHaveBeenCalledTimes(1)
      expect(flow.reopenCreatedAccountForEdit).toHaveBeenCalledWith('acc-chat-create')
      expect(flow.modalOpen.value).toBe(true)
      expect(vi.mocked(message.success)).toHaveBeenCalledWith('账户已创建，支持模型与检查项已自动配置，可继续调整')
    } finally {
      createSpy.mockRestore()
    }
  })

  it('媒体域（video）：行为不变——仍走托管创建路由，但不转编辑、走旧文案、关闭弹窗', async () => {
    const createSpy = vi.spyOn(api.myGeminiOAuth, 'createFromCode')
      .mockResolvedValue({ id: 'acc-media-create', status: 'active' })
    const flow = mountedOAuthCreateFlow('video')
    try {
      await flow.ctx.saveAccount()
      expect(createSpy).toHaveBeenCalledTimes(1)
      expect(flow.reopenCreatedAccountForEdit).not.toHaveBeenCalled()
      expect(flow.loadData).toHaveBeenCalledTimes(1)
      expect(flow.modalOpen.value).toBe(false)
      expect(vi.mocked(message.success)).toHaveBeenCalledWith('OAuth 账户已创建并启用')
    } finally {
      createSpy.mockRestore()
    }
  })
})

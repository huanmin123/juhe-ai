import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, createApp, defineComponent, h, nextTick, ref, watch, type Ref } from 'vue'

// v8-D 收敛链路只依赖预取缓存的同步读取：mock 掉 IO 层，缓存命中场景由用例注入。
const { cachedAvailabilityMock } = vi.hoisted(() => ({
  cachedAvailabilityMock: vi.fn<(input: unknown) => ReadonlySet<string> | undefined>()
}))
vi.mock('./mediaDomainModelAvailability', () => ({
  cachedMediaDomainModelAvailability: (input: unknown) => cachedAvailabilityMock(input)
}))

import {
  MINIMAX_OPENAI_V1_PROFILE_ID,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_TTS_FAMILY,
  OPENAI_VIDEO_GENERATION_FAMILY,
  VOLCENGINE_OPENAI_V1_PROFILE_ID
} from '@/shared/providerProtocol'
import type { ProviderDefinition, ProviderProtocolProfileDefinition } from '@/types/domain'
import { applyMediaCreateDefaults } from './accountMediaCreateDefaults'
import {
  applyProviderDefinitionDefaultsAfterLoad,
  useAccountEditForm,
  type ProviderDefaultState,
  type UseAccountEditFormOptions
} from './useAccountEditForm'

// ---- 夹具：基座外媒体供应商（FALLBACK_PROVIDERS 与轻量 options 均不含） ----

function mediaProfile(input: { id: string; providerCode: string; baseUrl: string; families: Array<{ code: string; name: string }> }): ProviderProtocolProfileDefinition {
  return {
    id: input.id,
    providerCode: input.providerCode,
    name: `${input.providerCode} OpenAI v1`,
    enabled: true,
    protocolCode: 'openai',
    protocolVersion: 'v1',
    baseUrl: input.baseUrl,
    defaultHealthCheckModel: '',
    accountTypes: ['api_key'],
    capabilities: [],
    endpointFamilies: input.families
  }
}

function mediaProviderDefinition(input: Parameters<typeof mediaProfile>[0]): ProviderDefinition {
  const profile = mediaProfile(input)
  return {
    id: `provider-${input.providerCode}`,
    code: input.providerCode,
    name: `${input.providerCode} 名称`,
    enabled: true,
    defaultProtocolProfileId: profile.id,
    protocolCode: profile.protocolCode,
    protocolVersion: profile.protocolVersion,
    baseUrl: profile.baseUrl,
    defaultHealthCheckModel: '',
    defaultSupportedModels: [],
    accountTypes: profile.accountTypes,
    capabilities: [],
    protocolProfiles: [profile]
  }
}

const volcengineDefinition = mediaProviderDefinition({
  id: VOLCENGINE_OPENAI_V1_PROFILE_ID,
  providerCode: 'volcengine',
  baseUrl: 'https://ark.cn-beijing.volces.com/api/v3',
  families: [
    { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' },
    { code: OPENAI_VIDEO_GENERATION_FAMILY, name: 'Video Generation' },
    { code: OPENAI_TTS_FAMILY, name: 'TTS' }
  ]
})

const minimaxDefinition = mediaProviderDefinition({
  id: MINIMAX_OPENAI_V1_PROFILE_ID,
  providerCode: 'minimax',
  baseUrl: 'https://api.minimaxi.chat/v1',
  families: [
    { code: OPENAI_VIDEO_GENERATION_FAMILY, name: 'Video Generation' },
    { code: OPENAI_TTS_FAMILY, name: 'TTS' }
  ]
})

const VIDEO_GROUP = ['video_create', 'video_get', 'video_content', 'video_cancel']

// ---- 受控 detail 加载器：模拟 ensureProviderDefinition 的「全局代次顶掉在途
// 请求 → 被顶掉的请求以 undefined 结束」语义（B3 第二入口）。 ----

function racingDefinitionLoader(definitions: ProviderDefinition[]) {
  const byCode = new Map(definitions.map((definition) => [definition.code, definition]))
  const settleFns = new Map<string, (definition: ProviderDefinition) => void>()
  let providerDefinitionsRef: Ref<ProviderDefinition[]> | undefined
  let requestSeq = 0
  return {
    // 绑定表单实际读取的完整定义缓存（useAccountListData.providerDefinitions 的替身）。
    bind(providerDefinitions: Ref<ProviderDefinition[]>): void {
      providerDefinitionsRef = providerDefinitions
    },
    ensure: (code: string): Promise<ProviderDefinition | undefined> => {
      const requestId = ++requestSeq
      return new Promise((resolve) => {
        settleFns.set(code, (definition) => {
          // 全局代次被后续请求顶掉：本请求不落缓存、以 undefined 结束。
          if (requestId !== requestSeq) {
            resolve(undefined)
            return
          }
          if (providerDefinitionsRef) {
            providerDefinitionsRef.value = [
              ...providerDefinitionsRef.value.filter((provider) => provider.code !== code),
              definition
            ]
          }
          resolve(definition)
        })
      })
    },
    settle: (code: string) => {
      const definition = byCode.get(code)
      if (!definition) throw new Error(`夹具缺少供应商定义：${code}`)
      settleFns.get(code)?.(definition)
    }
  }
}

// ---- 挂载 harness：useAccountEditForm + 模拟媒体表单的域默认 watch
//（与 AccountMediaForm 相同的触发源，复用提取后的同一纯函数）。 ----

type EditFormContext = ReturnType<typeof useAccountEditForm>

function mountMediaEditForm(
  buildEnsure: (providerDefinitions: Ref<ProviderDefinition[]>) => UseAccountEditFormOptions['ensureProviderDefinition'],
  capabilityDomain: Ref<'chat' | 'video' | 'audio' | 'image'> = ref('chat')
) {
  const providers = ref<ProviderDefinition[]>([])
  const providerDefinitions = ref<ProviderDefinition[]>([])
  const accounts = ref([])
  const groups = ref([])
  const systemAccounts = ref([])
  let context: EditFormContext | undefined
  const app = createApp(defineComponent({
    setup() {
      context = useAccountEditForm({
        accountScopeParams: computed(() => undefined),
        accounts,
        capabilityDomain: computed(() => capabilityDomain.value),
        extractApiErrorMessage: (_error, fallback) => fallback,
        groupIdForAccount: () => undefined,
        groups,
        isManagementView: computed(() => false),
        ensureProviderDefinition: buildEnsure(providerDefinitions),
        loadGroupOptions: async () => {},
        loadData: async () => {},
        refreshAccountMutationRows: async () => {},
        providerDefinitions,
        providers,
        systemAccounts
      })
      // 模拟 AccountMediaForm 新建态域默认 watch（编辑态不重放；用例不触发
      // 域外类型回退分支，故省略 select-type-choice 回退）。
      watch(
        [context.modalOpen, context.editingId, () => context!.form.providerCode, () => context!.form.providerProtocolProfileId, () => context!.form.type],
        () => {
          if (!context!.modalOpen.value || context!.editingId.value) return
          if (context!.form.type && context!.form.type !== 'api_key') return
          applyMediaCreateDefaults(context!.form, {
            capabilityDomain: 'video',
            providerCode: context!.form.providerCode,
            profile: context!.selectedProtocolProfile.value,
            type: context!.form.type
          })
        }
      )
      return () => h('div')
    }
  }))
  const container = document.createElement('div')
  document.body.appendChild(container)
  app.mount(container)
  return {
    ctx: context as EditFormContext,
    unmount: () => {
      app.unmount()
      container.remove()
    }
  }
}

async function flushTicks(times = 3): Promise<void> {
  for (let index = 0; index < times; index += 1) {
    await nextTick()
  }
}

beforeEach(() => {
  cachedAvailabilityMock.mockReset()
})

afterEach(() => {
  document.body.innerHTML = ''
})

describe('useAccountEditForm.selectProvider（B3：媒体供应商默认值落位）', () => {
  it('基座外媒体供应商首次选择（volcengine）：detail 返回后 type/baseUrl/域默认检查形态落位', async () => {
    const loader = racingDefinitionLoader([volcengineDefinition])
    const { ctx, unmount } = mountMediaEditForm((providerDefinitions) => {
      loader.bind(providerDefinitions)
      return loader.ensure
    })
    try {
      await ctx.openCreate()
      ctx.selectProvider('volcengine')
      await flushTicks()
      loader.settle('volcengine')
      await flushTicks()
      expect(ctx.form.providerCode).toBe('volcengine')
      expect(ctx.form.type).toBe('api_key')
      expect(ctx.form.providerProtocolProfileId).toBe(VOLCENGINE_OPENAI_V1_PROFILE_ID)
      expect(ctx.form.baseUrl).toBe('https://ark.cn-beijing.volces.com/api/v3')
      expect(ctx.form.supportedEndpointModes).toEqual(VIDEO_GROUP)
      expect(ctx.form.healthCheckEndpointMode).toBe('video_create')
      expect(ctx.form.status).toBe('active')
      // 凭据段可见性条件（AccountApiKeySection v-if form.type === 'api_key'）。
      expect(ctx.isApiKeyForm.value).toBe(true)
    } finally {
      unmount()
    }
  })

  it('detail 在途的用户输入按字段保留，未触碰字段仍回填（字段级 pristine）', async () => {
    const loader = racingDefinitionLoader([volcengineDefinition])
    const { ctx, unmount } = mountMediaEditForm((providerDefinitions) => {
      loader.bind(providerDefinitions)
      return loader.ensure
    })
    try {
      await ctx.openCreate()
      ctx.selectProvider('volcengine')
      await flushTicks()
      ctx.form.baseUrl = 'https://user.example/v1'
      loader.settle('volcengine')
      await flushTicks()
      expect(ctx.form.baseUrl).toBe('https://user.example/v1')
      expect(ctx.form.type).toBe('api_key')
      expect(ctx.form.providerProtocolProfileId).toBe(VOLCENGINE_OPENAI_V1_PROFILE_ID)
      expect(ctx.form.healthCheckEndpointMode).toBe('video_create')
      expect(ctx.isApiKeyForm.value).toBe(true)
    } finally {
      unmount()
    }
  })

  it('快速二次切换：被顶掉的 volcengine 静默让位，后选 minimax 默认值正确落位', async () => {
    const loader = racingDefinitionLoader([volcengineDefinition, minimaxDefinition])
    const { ctx, unmount } = mountMediaEditForm((providerDefinitions) => {
      loader.bind(providerDefinitions)
      return loader.ensure
    })
    try {
      await ctx.openCreate()
      ctx.selectProvider('volcengine')
      await flushTicks()
      ctx.selectProvider('minimax')
      await flushTicks()
      // volcengine 的在途请求此时已被 minimax 请求顶掉：落 undefined，不得污染表单。
      loader.settle('volcengine')
      await flushTicks()
      loader.settle('minimax')
      await flushTicks()
      expect(ctx.form.providerCode).toBe('minimax')
      expect(ctx.form.type).toBe('api_key')
      expect(ctx.form.providerProtocolProfileId).toBe(MINIMAX_OPENAI_V1_PROFILE_ID)
      expect(ctx.form.baseUrl).toBe('https://api.minimaxi.chat/v1')
      expect(ctx.form.supportedEndpointModes).toEqual(VIDEO_GROUP)
      expect(ctx.form.healthCheckEndpointMode).toBe('video_create')
      expect(ctx.form.status).toBe('active')
      expect(ctx.isApiKeyForm.value).toBe(true)
    } finally {
      unmount()
    }
  })
})

describe('applyProviderDefinitionDefaultsAfterLoad（B3③：definition 缺失不静默）', () => {
  const initialDefaults: ProviderDefaultState = {
    providerProtocolProfileId: '',
    type: '',
    baseUrl: '',
    clientCompatibility: 'openai_standard',
    supportedEndpointModes: ['chat_json'],
    healthCheckEndpointMode: 'chat_json',
    oauthMode: 'manual'
  }

  it('isCurrent 成立且首拍为空：重试一次后按权威默认值回填', async () => {
    let calls = 0
    const form: ProviderDefaultState = { ...initialDefaults, supportedEndpointModes: ['chat_json'] }
    await applyProviderDefinitionDefaultsAfterLoad({
      ensureDefinition: async () => {
        calls += 1
        return calls === 1 ? undefined : volcengineDefinition
      },
      form,
      initialDefaults,
      isCurrent: () => true,
      resolvedDefaults: () => ({
        providerProtocolProfileId: VOLCENGINE_OPENAI_V1_PROFILE_ID,
        type: 'api_key',
        baseUrl: volcengineDefinition.baseUrl,
        clientCompatibility: 'openai_standard',
        supportedEndpointModes: [],
        healthCheckEndpointMode: 'chat_json',
        oauthMode: 'manual'
      })
    })
    expect(calls).toBe(2)
    expect(form.type).toBe('api_key')
    expect(form.providerProtocolProfileId).toBe(VOLCENGINE_OPENAI_V1_PROFILE_ID)
    expect(form.baseUrl).toBe(volcengineDefinition.baseUrl)
  })

  it('重试后仍为空且 isCurrent 成立：上抛错误而非静默返回', async () => {
    const form: ProviderDefaultState = { ...initialDefaults, supportedEndpointModes: [] }
    await expect(applyProviderDefinitionDefaultsAfterLoad({
      ensureDefinition: async () => undefined,
      form,
      initialDefaults,
      isCurrent: () => true,
      resolvedDefaults: () => initialDefaults
    })).rejects.toThrow('供应商定义加载失败')
  })

  it('isCurrent 不成立（用户已切走）：静默放弃且不改写表单', async () => {
    let calls = 0
    const form: ProviderDefaultState = { ...initialDefaults, supportedEndpointModes: [] }
    await applyProviderDefinitionDefaultsAfterLoad({
      ensureDefinition: async () => {
        calls += 1
        return undefined
      },
      form,
      initialDefaults,
      isCurrent: () => false,
      resolvedDefaults: () => ({
        ...initialDefaults,
        providerProtocolProfileId: 'should-not-apply',
        type: 'api_key'
      })
    })
    expect(calls).toBe(1)
    expect(form.type).toBe('')
    expect(form.providerProtocolProfileId).toBe('')
  })
})

describe('useAccountEditForm.openCreate（v8-D：媒体域新建默认供应商同步收敛）', () => {
  it('视频域预取就绪且 GPT 无货：打开弹窗默认供应商直接落在首个有货供应商（glm）', async () => {
    cachedAvailabilityMock.mockReturnValue(new Set(['glm']))
    const { ctx, unmount } = mountMediaEditForm(() => async () => undefined, ref('video'))
    try {
      await ctx.openCreate()
      expect(cachedAvailabilityMock).toHaveBeenCalledWith(expect.objectContaining({
        capabilityDomain: 'video',
        isManagementView: false
      }))
      expect(ctx.form.providerCode).toBe('glm')
      expect(ctx.modalOpen.value).toBe(true)
    } finally {
      unmount()
    }
  })

  it('预取未就绪与全无货：回退既有全局默认（GPT 优先），对话域行为零变化', async () => {
    // 未就绪（undefined）。
    const pending = mountMediaEditForm(() => async () => undefined)
    try {
      await pending.ctx.openCreate()
      expect(pending.ctx.form.providerCode).toBe('gpt')
    } finally {
      pending.unmount()
    }
    // 交集为空（有货集合不含任何域内供应商）。
    cachedAvailabilityMock.mockReturnValue(new Set(['qwen']))
    const empty = mountMediaEditForm(() => async () => undefined)
    try {
      await empty.ctx.openCreate()
      expect(empty.ctx.form.providerCode).toBe('gpt')
    } finally {
      empty.unmount()
    }
    // chat 域不适用收敛（即使缓存命中）——对话域默认供应商行为零变化。
    cachedAvailabilityMock.mockReturnValue(new Set(['glm']))
    const chat = mountMediaEditForm(() => async () => undefined)
    try {
      await chat.ctx.openCreate()
      expect(chat.ctx.form.providerCode).toBe('gpt')
    } finally {
      chat.unmount()
    }
  })
})

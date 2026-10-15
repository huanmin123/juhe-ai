import { describe, expect, it } from 'vitest'

import { validateAccountModelMappings } from './accountSavePayload'
import type { AccountFormModel } from './accountFormTypes'
import { FALLBACK_PROVIDERS } from './accountOptions'

const mapping: AccountFormModel['modelMappings'][number] = {
  sourceModel: 'legacy-client-model',
  sourceEndpointFamily: 'chat_completions',
  upstreamModel: 'legacy-upstream-model',
  upstreamEndpointFamily: 'chat_completions',
  enabled: true
}
const openAIProfile = FALLBACK_PROVIDERS.find((provider) => provider.code === 'openai')!

describe('validateAccountModelMappings baseline changes', () => {
  it('revalidates an enabled-only change against current model candidates', () => {
    const changed = { ...mapping, enabled: false }

    const error = validateAccountModelMappings(
      [changed],
      [],
      ['chat_json'],
      openAIProfile,
      {
        baselineMappings: [mapping],
        mappingCurrentProviderSourceModelOptions: [],
        mappingUpstreamModelOptions: []
      }
    )

    expect(error).toContain('模型映射的上游模型必须从支持模型中选择：legacy-upstream-model')
  })

  it('keeps an unchanged baseline row exempt from the current candidate window', () => {
    expect(validateAccountModelMappings([mapping], [], ['chat_json'], openAIProfile, { baselineMappings: [mapping] })).toBeUndefined()
  })
})

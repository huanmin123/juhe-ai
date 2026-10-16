import { describe, expect, it } from 'vitest'
import { canSelectTrustedModelCheckAccount, mergeModelCheckRunModelOptions, modelCheckModelsForAccount } from './modelCheckProviderCapabilities'
import type { ModelCheckOption } from '@/types/domain/model-checks'

const supportedModels: ModelCheckOption[] = [
  { label: 'gpt-5.6-sol', value: 'gpt-5.6-sol' },
  { label: 'gpt-5.6-terra', value: 'gpt-5.6-terra' },
  { label: 'gpt-5.6-luna', value: 'gpt-5.6-luna' },
  { label: 'gpt-5.5', value: 'gpt-5.5' },
  { label: 'gpt-5.4', value: 'gpt-5.4' }
]

describe('mergeModelCheckRunModelOptions', () => {
  it('keeps catalog order and labels for catalog models and appends out-of-catalog account models by model id', () => {
    const merged = mergeModelCheckRunModelOptions(supportedModels, [
      'gpt-5.6-sol',
      'deepseek-v4.1-flash',
      'grok-4.5',
      'grok-4.6'
    ])
    expect(merged.map((item) => item.value)).toEqual([
      'gpt-5.6-sol',
      'deepseek-v4.1-flash',
      'grok-4.5',
      'grok-4.6'
    ])
    expect(merged[0].label).toBe('gpt-5.6-sol')
    expect(merged[1].label).toBe('deepseek-v4.1-flash')
  })

  it('deduplicates models registered both in the catalog and the account supported list', () => {
    const merged = mergeModelCheckRunModelOptions(supportedModels, [
      'gpt-5.6-sol',
      'gpt-5.6-sol',
      'gpt-5.6-terra'
    ])
    expect(merged.map((item) => item.value)).toEqual(['gpt-5.6-sol', 'gpt-5.6-terra'])
  })

  it('keeps a pure catalog account unchanged', () => {
    const merged = mergeModelCheckRunModelOptions(supportedModels, ['gpt-5.5', 'gpt-5.4'])
    expect(merged.map((item) => item.value)).toEqual(['gpt-5.5', 'gpt-5.4'])
    expect(merged.every((item) => item.label === item.value)).toBe(true)
  })

  it('appends only account models when none intersect the catalog', () => {
    const merged = mergeModelCheckRunModelOptions(supportedModels, ['deepseek-v4.1-flash'])
    expect(merged.map((item) => ({ label: item.label, value: item.value }))).toEqual([
      { label: 'deepseek-v4.1-flash', value: 'deepseek-v4.1-flash' }
    ])
  })

  it('returns an empty candidate list for an account without models', () => {
    expect(mergeModelCheckRunModelOptions(supportedModels, [])).toEqual([])
  })
})

describe('modelCheckModelsForAccount', () => {
  it('prefers the backend-provided merged modelCheckModels list', () => {
    const account = {
      id: 'a1',
      name: 'Alpha',
      providerCode: 'openai',
      providerProtocolProfileId: 'profile_openai_openai_v1',
      modelCheckModels: ['gpt-5.6-sol', 'deepseek-v4.1-flash']
    }
    expect(modelCheckModelsForAccount(account)).toEqual(['gpt-5.6-sol', 'deepseek-v4.1-flash'])
  })

  it('falls back to the catalog rule when the backend list is absent', () => {
    const account = {
      id: 'a1',
      name: 'Alpha',
      providerCode: 'openai',
      providerProtocolProfileId: 'profile_openai_openai_v1'
    }
    expect(modelCheckModelsForAccount(account)).toEqual([
      'gpt-5.6-sol',
      'gpt-5.6-terra',
      'gpt-5.6-luna',
      'gpt-5.5',
      'gpt-5.4'
    ])
  })
})

describe('canSelectTrustedModelCheckAccount', () => {
  const target = {
    id: 'target',
    name: '目标账户',
    providerCode: 'openai',
    providerProtocolProfileId: 'profile_openai_openai_v1',
    modelCheckModels: ['gpt-5.6-sol', 'gpt-6.1-sol']
  }
  const catalogModel = 'gpt-5.6-sol'
  const outOfCatalogModel = 'gpt-6.1-sol'

  it('admits a same-profile candidate with unknown models (list responses carry no modelCheckModels)', () => {
    const candidate = {
      id: 'peer',
      name: '对比账户',
      providerCode: 'openai',
      providerProtocolProfileId: 'profile_openai_openai_v1'
    }
    expect(canSelectTrustedModelCheckAccount(candidate, {
      targetAccount: target,
      model: outOfCatalogModel,
      excludedAccountId: target.id
    })).toBe(true)
  })

  it('keeps the precise model gate once the candidate models are known', () => {
    const supporting = { ...target, id: 'peer-known-support', name: '已支持对比' }
    const lacking = { ...target, id: 'peer-known-lacking', name: '不支持对比', modelCheckModels: ['gpt-5.6-sol'] }
    expect(canSelectTrustedModelCheckAccount(supporting, {
      targetAccount: target, model: outOfCatalogModel, excludedAccountId: target.id
    })).toBe(true)
    expect(canSelectTrustedModelCheckAccount(lacking, {
      targetAccount: target, model: outOfCatalogModel, excludedAccountId: target.id
    })).toBe(false)
  })

  it('still rejects cross-profile candidates and the target itself', () => {
    const crossProfile = {
      id: 'peer-cross',
      name: '跨档案账户',
      providerCode: 'glm',
      providerProtocolProfileId: 'profile_glm_general_openai_v1'
    }
    const self = { ...target, id: target.id }
    expect(canSelectTrustedModelCheckAccount(crossProfile, {
      targetAccount: target, model: catalogModel, excludedAccountId: target.id
    })).toBe(false)
    expect(canSelectTrustedModelCheckAccount(self, {
      targetAccount: target, model: catalogModel, excludedAccountId: target.id
    })).toBe(false)
  })
})

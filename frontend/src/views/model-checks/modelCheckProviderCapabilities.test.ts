import { describe, expect, it } from 'vitest'
import { mergeModelCheckRunModelOptions, modelCheckModelsForAccount } from './modelCheckProviderCapabilities'
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

import { describe, expect, it } from 'vitest'

import { proxyLabelForId } from '@/shared/proxyLabelCache'
import type { AccountListItem } from '@/types/domain'
import { accountProxyDisplay, rememberAccountProxyLabel } from './accountProxyDisplay'

function accountRow(overrides: Partial<AccountListItem> = {}): AccountListItem {
  return {
    id: 'account-proxy-display',
    providerCode: 'gpt',
    name: '代理展示回归账户',
    type: 'api_key',
    credentials: {},
    status: 'active',
    concurrencyLimit: 1,
    rpmLimit: 0,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    schedulable: true,
    ...overrides
  } as AccountListItem
}

describe('accountProxyDisplay', () => {
  it('列表行带代理展示字段时优先于候选回退', () => {
    expect(accountProxyDisplay(accountRow({
      proxyProfileId: 'proxy-row-on',
      proxyProfileName: '行内代理',
      proxyProfileType: 'http',
      proxyProfileEnabled: true
    }), { id: 'proxy-fallback', name: '候选代理', type: 'socks5', enabled: true }))
      .toEqual({ id: 'proxy-row-on', name: '行内代理', type: 'http', enabled: true })
  })

  it('代理不可用且行内无展示字段时返回 undefined', () => {
    expect(accountProxyDisplay(accountRow({
      proxyProfileId: 'proxy-row-missing',
      proxyProfileUnavailable: true
    }))).toBeUndefined()
  })
})

describe('rememberAccountProxyLabel', () => {
  it('按候选同款标签格式回填缓存（名称（类型））', () => {
    rememberAccountProxyLabel(accountRow({
      proxyProfileId: 'proxy-label-enabled',
      proxyProfileName: '出海代理',
      proxyProfileType: 'socks5h',
      proxyProfileEnabled: true
    }))
    expect(proxyLabelForId('proxy-label-enabled')).toBe('出海代理（socks5h）')
  })

  it('停用代理回填为名称（类型，已停用）', () => {
    rememberAccountProxyLabel(accountRow({
      proxyProfileId: 'proxy-label-disabled',
      proxyProfileName: '停用代理',
      proxyProfileType: 'http',
      proxyProfileEnabled: false
    }))
    expect(proxyLabelForId('proxy-label-disabled')).toBe('停用代理（http，已停用）')
  })

  it('无代理 id 或无名称时不写入缓存', () => {
    rememberAccountProxyLabel(accountRow({ proxyProfileName: '无 id 代理' }))
    rememberAccountProxyLabel(accountRow({ proxyProfileId: 'proxy-label-no-name', proxyProfileEnabled: false }))
    expect(proxyLabelForId('proxy-label-no-name')).toBeUndefined()
  })
})

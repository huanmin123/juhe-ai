import { describe, expect, it } from 'vitest'

import { accountCreatedSuccessText } from './useAccountEditSaveFlow'

describe('accountCreatedSuccessText', () => {
  it('已选分组且账户启用时只返回基础文案', () => {
    expect(accountCreatedSuccessText('账户', true, true)).toBe('账户已创建并启用')
  })

  it('已选分组但等待后台检查时返回基础文案', () => {
    expect(accountCreatedSuccessText('OAuth 账户', false, true)).toBe('OAuth 账户已创建，等待后台检查')
  })

  it('未选分组时追加路由不可用提示', () => {
    expect(accountCreatedSuccessText('账户', true, false))
      .toBe('账户已创建并启用；未加入分组，暂无法被路由调用，可稍后在编辑中绑定')
  })

  it('未选分组且等待后台检查时同样追加提示', () => {
    expect(accountCreatedSuccessText('OAuth 账户', false, false))
      .toBe('OAuth 账户已创建，等待后台检查；未加入分组，暂无法被路由调用，可稍后在编辑中绑定')
  })
})

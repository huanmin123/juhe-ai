import { describe, expect, it } from 'vitest'

import { accountCreatedSuccessText } from './useAccountEditSaveFlow'

describe('accountCreatedSuccessText', () => {
  it('账户启用时返回基础文案', () => {
    expect(accountCreatedSuccessText('账户', true)).toBe('账户已创建并启用')
  })

  it('等待后台检查时返回基础文案', () => {
    expect(accountCreatedSuccessText('OAuth 账户', false)).toBe('OAuth 账户已创建，等待后台检查')
  })
})

import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/api/client'
import { chatGenerationRuntime } from '@/views/chat/chatGenerationRuntime'
import { clearChatPendingSubmission } from '@/views/chat/chatPendingSubmissionStorage'
import { drainChatConversationSyncAccount, invalidateChatConversationSyncAccount } from '@/views/chat/chatConversationSync'
import type { CurrentUserSummary } from '@/types/domain'
import {
  authState,
  changePassword,
  clearAuthState,
  loadCaptcha,
  loadCurrentUser,
  login,
  logout,
  updateProfile
} from './useAuth'

vi.mock('@/api/client', () => ({
  api: {
    auth: {
      me: vi.fn(),
      captcha: vi.fn(),
      login: vi.fn(),
      logout: vi.fn(),
      changePassword: vi.fn(),
      updateProfile: vi.fn()
    }
  }
}))

vi.mock('@/views/chat/chatGenerationRuntime', () => ({
  chatGenerationRuntime: { close: vi.fn() }
}))

vi.mock('@/views/chat/chatLocalCache', () => ({
  getDefaultChatLocalCache: vi.fn(() => ({ clearAccount: vi.fn() }))
}))

vi.mock('@/views/chat/chatPendingSubmissionStorage', () => ({
  clearChatPendingSubmission: vi.fn()
}))

vi.mock('@/views/chat/chatConversationSync', () => ({
  invalidateChatConversationSyncAccount: vi.fn(),
  drainChatConversationSyncAccount: vi.fn().mockResolvedValue(undefined)
}))

function user(partial: Partial<CurrentUserSummary> = {}): CurrentUserSummary {
  return { id: 'u1', username: 'alice', displayName: 'Alice', role: 'admin', mustChangePassword: false, ...partial }
}

function axios401(): unknown {
  return { isAxiosError: true, response: { status: 401 } }
}

beforeEach(() => {
  vi.clearAllMocks()
  clearAuthState()
})

describe('loadCurrentUser', () => {
  it('首次加载写入当前用户与登录派生状态', async () => {
    const alice = user()
    vi.mocked(api.auth.me).mockResolvedValue(alice)
    // currentUser 是深响应 ref，返回值为 reactive 代理，用内容断言。
    await expect(loadCurrentUser(true)).resolves.toEqual(alice)
    expect(authState.currentUser.value).toEqual(alice)
    expect(authState.authChecked.value).toBe(true)
    expect(authState.isLoggedIn.value).toBe(true)
    expect(authState.isAdmin.value).toBe(true)
    expect(authState.isSuperAdmin.value).toBe(false)
  })

  it('authChecked 后默认使用缓存不再请求', async () => {
    const alice = user()
    authState.currentUser.value = alice
    await expect(loadCurrentUser()).resolves.toEqual(alice)
    expect(api.auth.me).not.toHaveBeenCalled()
  })

  it('force 强制重新请求', async () => {
    const first = user()
    const second = user({ displayName: '更新' })
    vi.mocked(api.auth.me).mockResolvedValueOnce(first).mockResolvedValueOnce(second)
    await loadCurrentUser(true)
    await loadCurrentUser(true)
    expect(api.auth.me).toHaveBeenCalledTimes(2)
    expect(authState.currentUser.value).toEqual(second)
  })

  it('401 清空登录状态', async () => {
    authState.currentUser.value = user()
    vi.mocked(api.auth.me).mockRejectedValue(axios401())
    await expect(loadCurrentUser(true)).resolves.toBeUndefined()
    expect(authState.currentUser.value).toBeUndefined()
    expect(authState.authChecked.value).toBe(true)
    expect(authState.isLoggedIn.value).toBe(false)
  })

  it('非 401 错误时保留已有用户返回', async () => {
    const alice = user()
    authState.currentUser.value = alice
    vi.mocked(api.auth.me).mockRejectedValue(new Error('network down'))
    await expect(loadCurrentUser(true)).resolves.toEqual(alice)
    expect(authState.currentUser.value).toEqual(alice)
  })

  it('非 401 错误且无已有用户时抛出原始错误', async () => {
    vi.mocked(api.auth.me).mockRejectedValue(new Error('network down'))
    await expect(loadCurrentUser(true)).rejects.toThrow('network down')
  })

  it('用户身份关键信息变化时递增 authRevision', async () => {
    const revisionBefore = authState.revision.value
    vi.mocked(api.auth.me).mockResolvedValueOnce(user({ id: 'u1' }))
    await loadCurrentUser(true)
    expect(authState.revision.value).toBe(revisionBefore + 1)
    vi.mocked(api.auth.me).mockResolvedValueOnce(user({ id: 'u1', role: 'super_admin' }))
    await loadCurrentUser(true)
    expect(authState.revision.value).toBe(revisionBefore + 2)
    // 同一用户同角色同 mustChangePassword 不递增。
    vi.mocked(api.auth.me).mockResolvedValueOnce(user({ id: 'u1', role: 'super_admin' }))
    await loadCurrentUser(true)
    expect(authState.revision.value).toBe(revisionBefore + 2)
  })
})

describe('login / logout', () => {
  it('login 成功后应用当前用户', async () => {
    const alice = user()
    vi.mocked(api.auth.login).mockResolvedValue(alice)
    await expect(login({ username: 'alice', password: 'pw' })).resolves.toEqual(alice)
    expect(authState.currentUser.value).toEqual(alice)
    expect(api.auth.login).toHaveBeenCalledWith({ username: 'alice', password: 'pw' })
  })

  it('logout 清理聊天状态并清空登录', async () => {
    const alice = user({ id: 'acc-1' })
    authState.currentUser.value = alice
    vi.mocked(api.auth.logout).mockResolvedValue({ loggedOut: true })
    await logout()
    expect(api.auth.logout).toHaveBeenCalledTimes(1)
    expect(invalidateChatConversationSyncAccount).toHaveBeenCalledWith('acc-1')
    expect(chatGenerationRuntime.close).toHaveBeenCalledWith('acc-1')
    expect(clearChatPendingSubmission).toHaveBeenCalledWith(window.sessionStorage, 'acc-1')
    expect(drainChatConversationSyncAccount).toHaveBeenCalledWith('acc-1')
    expect(authState.currentUser.value).toBeUndefined()
    expect(authState.authChecked.value).toBe(true)
  })
})

describe('其他账户操作', () => {
  it('loadCaptcha 透传验证码接口', async () => {
    const captcha = { required: false }
    vi.mocked(api.auth.captcha).mockResolvedValue(captcha)
    await expect(loadCaptcha()).resolves.toBe(captcha)
  })

  it('changePassword 成功后更新当前用户', async () => {
    const updated = user({ mustChangePassword: false })
    vi.mocked(api.auth.changePassword).mockResolvedValue(updated)
    await expect(changePassword({ newPassword: 'new-pw' })).resolves.toEqual(updated)
    expect(authState.currentUser.value).toEqual(updated)
  })

  it('updateProfile 成功后更新当前用户', async () => {
    const updated = user({ displayName: '新昵称' })
    vi.mocked(api.auth.updateProfile).mockResolvedValue(updated)
    await expect(updateProfile({ displayName: '新昵称' })).resolves.toEqual(updated)
    expect(authState.currentUser.value).toEqual(updated)
  })
})

describe('clearAuthState', () => {
  it('清空用户并标记已检查', () => {
    authState.currentUser.value = user()
    const revisionBefore = authState.revision.value
    clearAuthState()
    expect(authState.currentUser.value).toBeUndefined()
    expect(authState.authChecked.value).toBe(true)
    expect(authState.revision.value).toBe(revisionBefore + 1)
  })

  it('无用户时不递增 revision', () => {
    const revisionBefore = authState.revision.value
    clearAuthState()
    expect(authState.revision.value).toBe(revisionBefore)
  })
})

import type { RequestAuthContext } from './request-context.js'

import { runtimeConfig } from '../../config/runtime.js'
import { findSystemAccountByUsernameAsync } from '../../storage/repositories.js'

export async function developmentAutoLoginContextAsync(): Promise<RequestAuthContext | undefined> {
  const username = runtimeConfig.development.autoLoginUsername
  if (!username) return undefined

  const account = await findSystemAccountByUsernameAsync(username)
  if (!account || account.status !== 'active') {
    throw new Error(`开发自动登录账户不存在或未启用：${username}`)
  }

  return {
    systemAccountId: account.id,
    username: account.username,
    displayName: account.displayName,
    role: account.role,
    mustChangePassword: false,
    sessionId: 'development-auto-login'
  }
}

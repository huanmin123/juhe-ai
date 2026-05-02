import { AsyncLocalStorage } from 'node:async_hooks'

export interface RequestAuthContext {
  systemAccountId: string
  role: 'admin' | 'user'
  username: string
  displayName: string
  mustChangePassword: boolean
  sessionId: string
}

const requestAuthContext = new AsyncLocalStorage<RequestAuthContext | undefined>()

export function withRequestAuthContext<T>(context: RequestAuthContext | undefined, handler: () => T): T {
  return requestAuthContext.run(context, handler)
}

export function getRequestAuthContext(): RequestAuthContext | undefined {
  return requestAuthContext.getStore()
}

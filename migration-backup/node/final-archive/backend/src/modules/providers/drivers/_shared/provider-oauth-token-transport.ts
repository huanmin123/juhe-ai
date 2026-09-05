import { AsyncLocalStorage } from 'node:async_hooks'

import {
  requestTokenExchange,
  type TokenExchangeTransport,
  type TokenExchangeTransportInput,
  type TokenExchangeTransportResponse
} from './token-exchange-transport.js'

const testTransportContext = new AsyncLocalStorage<TokenExchangeTransport>()
let processTestTransport: TokenExchangeTransport | undefined

export function requestProviderOAuthToken(
  input: TokenExchangeTransportInput
): Promise<TokenExchangeTransportResponse> {
  return (testTransportContext.getStore() ?? processTestTransport ?? requestTokenExchange)(input)
}

export function runWithProviderOAuthTokenTransportForTest<T>(
  transport: TokenExchangeTransport,
  task: () => T
): T {
  return testTransportContext.run(transport, task)
}

/** 仅供跨 HTTP 边界的隔离回归使用；调用方必须在 finally 中清除。 */
export function setProviderOAuthTokenTransportForTest(transport?: TokenExchangeTransport): void {
  processTestTransport = transport
}

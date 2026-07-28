import { AsyncLocalStorage } from 'node:async_hooks'

import {
  requestTokenExchange,
  type TokenExchangeTransport,
  type TokenExchangeTransportInput,
  type TokenExchangeTransportResponse
} from './token-exchange-transport.js'

const testTransportContext = new AsyncLocalStorage<TokenExchangeTransport>()

export function requestProviderOAuthToken(
  input: TokenExchangeTransportInput
): Promise<TokenExchangeTransportResponse> {
  return (testTransportContext.getStore() ?? requestTokenExchange)(input)
}

export function runWithProviderOAuthTokenTransportForTest<T>(
  transport: TokenExchangeTransport,
  task: () => T
): T {
  return testTransportContext.run(transport, task)
}

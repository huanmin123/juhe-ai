import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import type { GatewayAccountModelPriority } from '../dispatch/model-filter.js'
import type { OpenAIGatewayClientStrategyContext } from './strategy.js'

import {
  orderOpenAIAccountsByCodexTurnAvoidance,
  orderOpenAIAccountsByCodexTurnAvoidanceAsync,
  rememberCodexTurnStreamFailure,
  rememberCodexTurnStreamFailureAsync,
  type CodexTurnAccountAvoidanceResult,
  type CodexTurnFailureEvidence,
  type CodexTurnFailureRecordResult
} from './codex-turn-retry.service.js'

// The persistence implementation originated as Codex turn retry. Its state
// key is now supplied by the common source resolver; these names keep callers
// from coupling new providers to the legacy implementation detail.
export type ClientSourceFailureEvidence = CodexTurnFailureEvidence
export type ClientSourceAccountAvoidanceResult = CodexTurnAccountAvoidanceResult
export type ClientSourceFailureRecordResult = CodexTurnFailureRecordResult

export function orderOpenAIAccountsByClientSourceAvoidance(
  accounts: UpstreamAccount[],
  strategy: OpenAIGatewayClientStrategyContext,
  modelPriority?: GatewayAccountModelPriority
): ClientSourceAccountAvoidanceResult {
  return orderOpenAIAccountsByCodexTurnAvoidance(accounts, strategy, modelPriority)
}

export async function orderOpenAIAccountsByClientSourceAvoidanceAsync(
  accounts: UpstreamAccount[],
  strategy: OpenAIGatewayClientStrategyContext,
  modelPriority?: GatewayAccountModelPriority
): Promise<ClientSourceAccountAvoidanceResult> {
  return await orderOpenAIAccountsByCodexTurnAvoidanceAsync(accounts, strategy, modelPriority)
}

export function rememberGatewayClientSourceFailure(
  strategy: OpenAIGatewayClientStrategyContext,
  accountId: string,
  input: { errorCode?: string; message?: string; evidence?: ClientSourceFailureEvidence; observationId?: string } = {}
): ClientSourceFailureRecordResult | undefined {
  return rememberCodexTurnStreamFailure(strategy, accountId, input)
}

export async function rememberGatewayClientSourceFailureAsync(
  strategy: OpenAIGatewayClientStrategyContext,
  accountId: string,
  input: { errorCode?: string; message?: string; evidence?: ClientSourceFailureEvidence; observationId?: string } = {}
): Promise<ClientSourceFailureRecordResult | undefined> {
  return await rememberCodexTurnStreamFailureAsync(strategy, accountId, input)
}

export interface CodexBridgeToolIdentityInput {
  adapterKind: 'function' | 'custom'
  idPrefix: string
  index: number
  upstreamCallId?: string
  suffix: string
}

export interface CodexBridgeToolIdentity {
  itemId: string
  callId: string
  itemType: 'function_call' | 'custom_tool_call'
}

export function createCodexBridgeToolIdentity(
  input: CodexBridgeToolIdentityInput
): CodexBridgeToolIdentity {
  const itemPrefix = input.adapterKind === 'custom' ? 'ctc' : 'fc'
  return {
    itemId: `${itemPrefix}_${input.idPrefix}_${input.index}_${input.suffix}`,
    callId: input.upstreamCallId ?? `call_${input.idPrefix}_${input.index}_${input.suffix}`,
    itemType: input.adapterKind === 'custom' ? 'custom_tool_call' : 'function_call'
  }
}

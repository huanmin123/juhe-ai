import { describe, expect, it } from 'vitest'

import { chatErrorMessage } from './chatErrorMessage'

// 会话锁定相关错误码映射（AI 问答简化设计 D5）：映射兜底文案；服务端
// message 优先由 statusLabel 的 errorMessage || chatErrorMessage(errorCode) 链保证。
describe('chatErrorMessage 锁定错误码映射', () => {
  it('chat_model_locked 映射为换模型引导文案', () => {
    expect(chatErrorMessage('chat_model_locked')).toBe('当前会话已绑定模型，如需更换请新建会话')
  })

  it('chat_conversation_locked 映射为切账户引导文案', () => {
    expect(chatErrorMessage('chat_conversation_locked')).toBe('会话已锁定，切换账户请新建会话')
  })

  it('chat_conversation_binding_changed 映射为刷新提示文案', () => {
    expect(chatErrorMessage('chat_conversation_binding_changed')).toBe('会话绑定已变化，请刷新后重试')
  })

  it('未知与缺失错误码回退通用文案', () => {
    expect(chatErrorMessage(undefined)).toBe('生成任务异常结束，请重新发送')
    expect(chatErrorMessage('unknown_code')).toBe('生成任务异常结束，请重新发送')
  })
})

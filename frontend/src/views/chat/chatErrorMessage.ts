// 会话锁定相关错误码映射（AI 问答简化设计 D5）：服务端 message 优先，映射兜底。
// 生成媒体与站内工具体系已退场，其专属错误码映射一并删除。
const publicChatErrorMessages: Readonly<Record<string, string>> = Object.freeze({
  upstream_http_error: '模型服务请求失败，请稍后重试',
  upstream_stream_failed: '模型响应中断，请重新发送',
  stream_interrupted: '生成连接已中断，请重新发送',
  internal_generation_failed: '生成任务异常结束，请重新发送',
  chat_model_locked: '当前会话已绑定模型，如需更换请新建会话',
  chat_conversation_locked: '会话已锁定，切换账户请新建会话',
  chat_conversation_binding_changed: '会话绑定已变化，请刷新后重试'
})

export function chatErrorMessage(errorCode: string | undefined): string {
  return (errorCode && publicChatErrorMessages[errorCode]) || '生成任务异常结束，请重新发送'
}

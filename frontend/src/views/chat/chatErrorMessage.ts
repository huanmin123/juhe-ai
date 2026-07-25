const publicChatErrorMessages: Readonly<Record<string, string>> = Object.freeze({
  upstream_http_error: '模型服务请求失败，请稍后重试',
  upstream_stream_failed: '模型响应中断，请重新发送',
  image_generation_failed: '图片生成失败，请重新发送',
  image_generation_not_enabled: '图片生成失败：可用上游分组未开通图片生成功能',
  image_generation_permission_denied: '图片生成失败：上游拒绝了图片生成权限',
  image_generation_rate_limited: '图片生成失败：上游请求过于频繁，请稍后重试',
  image_generation_request_rejected: '图片生成失败：上游拒绝了本次图片参数或内容',
  stream_interrupted: '生成连接已中断，请重新发送',
  internal_generation_failed: '生成任务异常结束，请重新发送'
})

export function chatErrorMessage(errorCode: string | undefined): string {
  return (errorCode && publicChatErrorMessages[errorCode]) || '生成任务异常结束，请重新发送'
}

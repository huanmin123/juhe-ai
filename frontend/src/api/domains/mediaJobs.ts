import type { MediaJobDetail, MediaJobListResult } from '@/types/domain'
import type { MediaJobListParams } from '../contracts'
import { apiUrl, http, unwrap } from '../http'

// contentTextRaw 实时取回任务产物内容（零资源存储任务——长音频转写——的
// 任务中心闭环面；服务端边取边回不落盘）。响应是原始字节非 kernel 信封，
// 会话 Cookie 鉴权；错误响应按信封 JSON 解出 message 抛出。
async function contentTextRaw(path: string): Promise<string> {
  const response = await fetch(apiUrl(path), { credentials: 'include' })
  if (!response.ok) {
    let message = `产物内容获取失败（HTTP ${response.status}）`
    try {
      const payload = (await response.json()) as { error?: { message?: string } }
      if (payload.error?.message) message = payload.error.message
    } catch {
      // 非 JSON 错误体保留默认消息
    }
    throw new Error(message)
  }
  return response.text()
}

export const mediaJobsApi = {
  list: (params?: MediaJobListParams) => unwrap<MediaJobListResult>(http.get('/media-jobs', { params })),
  detail: (id: string) => unwrap<MediaJobDetail>(http.get(`/media-jobs/${encodeURIComponent(id)}`)),
  contentText: (id: string) => contentTextRaw(`/media-jobs/${encodeURIComponent(id)}/content`),
  myList: (params?: MediaJobListParams) => unwrap<MediaJobListResult>(http.get('/my-media-jobs', { params })),
  myDetail: (id: string) => unwrap<MediaJobDetail>(http.get(`/my-media-jobs/${encodeURIComponent(id)}`)),
  myContentText: (id: string) => contentTextRaw(`/my-media-jobs/${encodeURIComponent(id)}/content`)
}

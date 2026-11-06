/**
 * 媒体产物下载地址的 scheme 白名单（安全审计 SEC-01）。
 *
 * 管理面详情把 artifact.contentUrl 直接渲染为 video/audio 的 src 与 a.href；
 * 后端已按 http/https 过滤存量与新增数据，前端再做一次防御性校验，避免
 * javascript:/data: 等可执行 scheme 或相对路径进入 DOM。
 */

/** 只接受 http/https 绝对地址；空值、相对路径与非法 scheme 一律返回空串。 */
export function safeArtifactContentUrl(value: string | null | undefined): string {
  const trimmed = (value ?? '').trim()
  if (!trimmed) return ''
  try {
    const parsed = new URL(trimmed)
    return parsed.protocol === 'http:' || parsed.protocol === 'https:' ? trimmed : ''
  } catch {
    return ''
  }
}

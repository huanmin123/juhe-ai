import { message } from '@/lib/antd'

export async function copyTextToClipboard(value: string, successMessage = '已复制'): Promise<boolean> {
  if (!value) return false
  if (!navigator.clipboard?.writeText) {
    message.error('当前浏览器不支持自动复制，请手动选择内容复制')
    return false
  }
  try {
    await navigator.clipboard.writeText(value)
    message.success(successMessage)
    return true
  } catch (error) {
    console.error(error)
    message.error('复制失败，请手动选择内容复制')
    return false
  }
}

import { message } from '@/lib/antd'

export async function copyTextToClipboard(value: string, successMessage = '已复制'): Promise<boolean> {
  if (!value) return false
  try {
    await writeTextToClipboard(value)
    message.success(successMessage)
    return true
  } catch (error) {
    console.error(error)
    const clipboard = typeof navigator === 'undefined' ? undefined : navigator.clipboard
    message.error(clipboard?.writeText ? resolveClipboardFailureMessage(error) : resolveClipboardUnavailableMessage())
    return false
  }
}

export async function writeTextToClipboard(value: string): Promise<void> {
  const clipboard = typeof navigator === 'undefined' ? undefined : navigator.clipboard
  let clipboardError: unknown
  if (clipboard?.writeText) {
    try {
      await clipboard.writeText(value)
      return
    } catch (error) {
      clipboardError = error
    }
  }
  if (copyTextWithSelectionFallback(value)) return
  throw clipboardError instanceof Error ? clipboardError : new Error('clipboard unavailable')
}

function copyTextWithSelectionFallback(value: string): boolean {
  if (typeof document === 'undefined' || typeof document.execCommand !== 'function' || !document.body) {
    return false
  }

  const textArea = document.createElement('textarea')
  const selection = document.getSelection()
  const selectedRanges: Range[] = []
  const activeElement = document.activeElement instanceof HTMLElement ? document.activeElement : null

  if (selection) {
    for (let index = 0; index < selection.rangeCount; index += 1) {
      selectedRanges.push(selection.getRangeAt(index))
    }
  }

  textArea.value = value
  textArea.setAttribute('readonly', 'true')
  textArea.style.position = 'fixed'
  textArea.style.left = '-9999px'
  textArea.style.top = '0'
  textArea.style.opacity = '0'
  document.body.appendChild(textArea)

  let copied = false
  try {
    textArea.focus({ preventScroll: true })
    textArea.select()
    textArea.setSelectionRange(0, value.length)
    copied = document.execCommand('copy')
  } catch (error) {
    console.error(error)
  } finally {
    textArea.remove()
    if (selection) {
      selection.removeAllRanges()
      selectedRanges.forEach((range) => selection.addRange(range))
    }
    activeElement?.focus({ preventScroll: true })
  }

  return copied
}

function resolveClipboardUnavailableMessage(): string {
  if (typeof window !== 'undefined' && window.isSecureContext === false) {
    return '当前页面不是 HTTPS 或本机地址，浏览器已限制自动复制，请手动选择内容复制'
  }
  return '当前环境不支持自动复制，请手动选择内容复制'
}

function resolveClipboardFailureMessage(error: unknown): string {
  if (typeof document !== 'undefined' && !document.hasFocus()) {
    return '当前页面未获得焦点，复制失败，请回到页面后重试'
  }
  if (isClipboardNotAllowedError(error)) {
    return '浏览器未允许本次复制，请直接点击复制按钮或手动选择内容复制'
  }
  return '复制失败，请手动选择内容复制'
}

function isClipboardNotAllowedError(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'NotAllowedError'
}

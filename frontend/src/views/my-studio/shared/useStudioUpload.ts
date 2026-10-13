import axios from 'axios'
import { ref } from 'vue'

import { studioApi } from '@/api/domains/studio'
import type { StudioAsset } from '@/types/domain'

/**
 * 工作室素材上传组合式函数（§8 强制点 6）：复用 chat uploadAsset 模式
 * （FormData + 进度 + timeout 0 + AbortSignal）；上传中可取消，取消后
 * 返回 undefined 且不产生孤立占位卡（调用方据此回收占位）。
 */
export function useStudioUpload() {
  const uploading = ref(false)
  const progress = ref(0)
  let controller: AbortController | undefined

  function isCancelledError(error: unknown): boolean {
    return axios.isAxiosError(error) && axios.isCancel(error)
  }

  async function upload(file: File, paramsJson?: Record<string, unknown>): Promise<StudioAsset | undefined> {
    controller?.abort()
    controller = new AbortController()
    uploading.value = true
    progress.value = 0
    try {
      return await studioApi.uploadAsset(file, {
        paramsJson,
        signal: controller.signal,
        onProgress: (percent) => {
          progress.value = percent
        }
      })
    } catch (error) {
      if (isCancelledError(error)) return undefined
      throw error
    } finally {
      uploading.value = false
      controller = undefined
    }
  }

  function cancel(): void {
    controller?.abort()
  }

  return { uploading, progress, upload, cancel }
}

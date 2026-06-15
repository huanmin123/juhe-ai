import { message } from '@/lib/antd'
import { ref } from 'vue'

import { api } from '@/api/client'
import type { ProviderModelOption } from '@/types/domain'

export function useUsageRecordModelOptions() {
  const modelOptions = ref<ProviderModelOption[]>([])
  const modelOptionsLoading = ref(false)
  let modelOptionsLoaded = false
  let modelOptionsLoadingPromise: Promise<void> | undefined

  async function loadModelOptions(force = false): Promise<void> {
    if (!force && (modelOptionsLoaded || modelOptionsLoadingPromise)) {
      return modelOptionsLoadingPromise
    }
    modelOptionsLoading.value = true
    modelOptionsLoadingPromise = (async () => {
      try {
        modelOptions.value = await api.providers.modelOptions()
        modelOptionsLoaded = true
      } catch (error) {
        console.error(error)
        modelOptionsLoaded = true
        message.warning('加载模型筛选选项失败')
      } finally {
        modelOptionsLoading.value = false
        modelOptionsLoadingPromise = undefined
      }
    })()
    return modelOptionsLoadingPromise
  }

  return {
    loadModelOptions,
    modelOptions,
    modelOptionsLoading
  }
}

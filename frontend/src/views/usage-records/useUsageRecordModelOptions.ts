import { message } from '@/lib/antd'
import { useProviderModelSelectOptions } from '@/composables/useProviderModelSelectOptions'

export function useUsageRecordModelOptions() {
  const resource = useProviderModelSelectOptions({
    onLoadError: (error) => {
      console.error(error)
      message.warning('加载模型筛选选项失败')
    }
  })

  return {
    loadModelOptions: resource.loadModelOptions,
    modelOptions: resource.providerModelOptions,
    modelOptionsLoading: resource.loading
  }
}

import { ref } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import type {
  ExternalIntegrationSourceSummary,
  ExternalIntegrationSourceTokenSummary
} from '@/types/domain'

export interface UseExternalSourceTokenActionsOptions {
  reload: () => Promise<void>
}

export function useExternalSourceTokenActions(options: UseExternalSourceTokenActionsOptions) {
  const createdTokenOpen = ref(false)
  const createdTokenPlain = ref('')
  const generatingTokenSourceId = ref('')
  const tokenCopyingKey = ref('')

  function showCreatedToken(token: string): void {
    createdTokenPlain.value = token
    createdTokenOpen.value = true
  }

  function clearCreatedToken(): void {
    createdTokenPlain.value = ''
    createdTokenOpen.value = false
  }

  function closeCreatedTokenModal(): void {
    clearCreatedToken()
  }

  async function resetBuiltInTestToken(): Promise<void> {
    try {
      const result = await api.externalIntegrationSources.resetBuiltInTestToken()
      await copyTextToClipboard(result.token.token, '内置测试 Token 已重置并复制')
      await options.reload()
    } catch (error) {
      message.error(extractApiErrorMessage(error, '重置内置测试 Token 失败'))
    }
  }

  async function generateSourceToken(record: ExternalIntegrationSourceSummary): Promise<void> {
    if (record.isBuiltIn || primaryToken(record) || generatingTokenSourceId.value) return
    generatingTokenSourceId.value = record.id
    try {
      const result = await api.externalIntegrationSources.createToken(record.id, {
        name: `${record.name} 生产 Token`,
        status: 'active',
        scopes: [...record.scopes],
        expiresAt: record.expiresAt ?? null
      })
      showCreatedToken(result.token.token)
      message.success('生产 Token 已生成')
      await options.reload()
    } catch (error) {
      message.error(extractApiErrorMessage(error, '生成生产 Token 失败'))
    } finally {
      if (generatingTokenSourceId.value === record.id) {
        generatingTokenSourceId.value = ''
      }
    }
  }

  async function copyTokenPreview(record: ExternalIntegrationSourceSummary): Promise<void> {
    const token = primaryToken(record)
    if (!token || tokenCopyingKey.value) return
    const copyingKey = tokenCopyKey(record)
    tokenCopyingKey.value = copyingKey
    try {
      const result = await api.externalIntegrationSources.tokenSecret(record.id, token.id)
      await copyTextToClipboard(result.token, '完整 Token 已复制')
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '复制完整 Token 失败'))
    } finally {
      if (tokenCopyingKey.value === copyingKey) {
        tokenCopyingKey.value = ''
      }
    }
  }

  return {
    createdTokenOpen,
    createdTokenPlain,
    generatingTokenSourceId,
    tokenCopyingKey,
    showCreatedToken,
    clearCreatedToken,
    closeCreatedTokenModal,
    resetBuiltInTestToken,
    generateSourceToken,
    copyTokenPreview,
    formatTokenPreview,
    tokenDisplayTitle,
    primaryToken,
    tokenCopyKey
  }
}

function formatTokenPreview(token: ExternalIntegrationSourceTokenSummary | undefined): string {
  if (!token) return '未生成'
  return maskSecretPreview('', token.tokenPrefix, token.tokenSuffix)
}

function tokenDisplayTitle(token: ExternalIntegrationSourceTokenSummary | undefined): string {
  return token ? '列表仅显示 Token 标识，点击复制按钮复制完整 Token' : '未生成'
}

function maskSecretPreview(value: string | undefined, prefix?: string, suffix?: string): string {
  if (value) {
    return value.length > 16 ? `${value.slice(0, 8)}...${value.slice(-8)}` : value
  }
  const head = prefix?.slice(0, 8) ?? ''
  const tail = suffix?.slice(-8) ?? ''
  if (head && tail) return `${head}...${tail}`
  if (head) return `${head}...`
  return '未生成'
}

function primaryToken(record: ExternalIntegrationSourceSummary): ExternalIntegrationSourceTokenSummary | undefined {
  return record.tokens[0]
}

function tokenCopyKey(record: ExternalIntegrationSourceSummary): string {
  const token = primaryToken(record)
  return token ? `${record.id}:${token.id}` : ''
}

import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')

const accountTestModalPath = resolve(frontendRoot, 'src/views/accounts/useAccountTestModal.ts')
const accountTestModelsPath = resolve(frontendRoot, 'src/views/accounts/useAccountTestModels.ts')

const accountTestModalSource = readFileSync(accountTestModalPath, 'utf8')
const accountTestModelsSource = readFileSync(accountTestModelsPath, 'utf8')

assertIncludes(accountTestModalSource, "import { useAccountTestModels } from './useAccountTestModels'", '账户测试弹窗应通过模型 composable 获取测试模型能力')
assertIncludes(accountTestModelsSource, 'export function useAccountTestModels', '模型 composable 应导出 useAccountTestModels')
assertIncludes(accountTestModelsSource, 'api.providers.models(requestProviderCode)', '模型 composable 应负责供应商模型列表加载')
assertIncludes(accountTestModelsSource, 'buildTestModelOptions', '模型 composable 应负责构建测试模型选项')
assertIncludes(accountTestModelsSource, 'providerDefaultTestModelForAccountSelection', '模型 composable 应负责供应商默认测试模型推导')
assertIncludes(accountTestModelsSource, 'nextTestModel', '模型 composable 应负责测试模型回落选择')
assertIncludes(accountTestModelsSource, 'GPT_VENDOR_CODE', '模型 composable 应负责 OpenAI 默认供应商回落')
assertIncludes(accountTestModelsSource, 'providerModelsProviderCode.value === providerCode', '模型 composable 应按供应商校验缓存归属')
assertIncludes(accountTestModelsSource, 'testTargetProviderCode.value || GPT_VENDOR_CODE', '模型 composable 应按当前测试目标校验请求是否仍有效')

assertNotIncludes(accountTestModalSource, 'api.providers.models', '账户测试弹窗不应直接加载供应商模型列表')
assertNotIncludes(accountTestModalSource, 'ProviderModelPricing', '账户测试弹窗不应持有供应商模型列表类型')
assertNotIncludes(accountTestModalSource, 'providerModelsProviderCode', '账户测试弹窗不应持有供应商模型缓存归属状态')
assertNotIncludes(accountTestModalSource, 'buildTestModelOptions', '账户测试弹窗不应直接构建测试模型选项')
assertNotIncludes(accountTestModalSource, 'providerDefaultTestModelForAccountSelection', '账户测试弹窗不应直接推导供应商默认测试模型')
assertNotIncludes(accountTestModalSource, 'isGatewaySupportedTestSelection', '账户测试弹窗不应直接判断测试目标协议兼容')
assertNotIncludes(accountTestModalSource, 'nextTestModel', '账户测试弹窗不应直接处理测试模型回落')
assertNotIncludes(accountTestModalSource, 'GPT_VENDOR_CODE', '账户测试弹窗不应直接持有 OpenAI 默认供应商回落')

console.log('账户测试模型 composable 回归通过：模型加载、缓存归属、默认模型与弹窗流程边界保持分离')

function assertIncludes(source: string, expected: string, message: string): void {
  if (!source.includes(expected)) {
    throw new Error(`${message}，未找到 ${expected}`)
  }
}

function assertNotIncludes(source: string, unexpected: string, message: string): void {
  if (source.includes(unexpected)) {
    throw new Error(`${message}，不应包含 ${unexpected}`)
  }
}

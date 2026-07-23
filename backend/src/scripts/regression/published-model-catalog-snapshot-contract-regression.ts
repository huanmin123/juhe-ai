import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const sourceFiles = [
  'server.ts',
  'modules/chat/chat.routes.ts',
  'modules/model-pricing/client-model-catalog.service.ts',
  'modules/providers/providers.routes.ts',
  'modules/system-accounts/system-accounts.routes.ts'
]
for (const file of sourceFiles) {
  const source = readFileSync(new URL(`../../${file}`, import.meta.url), 'utf8')
  assert.doesNotMatch(source, /published-model-catalog|gateway_model_catalog_snapshots|model_catalog_snapshot_rebuild|chat_list:|chat_model:/, `${file} 不得引用已退场发布快照链路`)
}

const chatRoutes = readFileSync(new URL('../../modules/chat/chat.routes.ts', import.meta.url), 'utf8')
assert.match(chatRoutes, /listClientModelCatalogAsync/, 'Chat 必须使用动态模型目录')
assert.match(chatRoutes, /chatRouter\.get\('\/conversations\/:conversationId\/models'/, 'Chat 模型列表接口必须存在')
assert.match(chatRoutes, /chatRouter\.get\('\/conversations\/:conversationId\/models\/:modelId'/, 'Chat 模型能力接口必须存在')

console.log('发布模型快照退场契约回归通过')

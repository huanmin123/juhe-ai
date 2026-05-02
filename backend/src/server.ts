import cors from 'cors'
import express from 'express'

import { accountsRouter } from './modules/accounts/accounts.routes.js'
import { apiKeysRouter } from './modules/api-keys/api-keys.routes.js'
import { errorPoliciesRouter } from './modules/error-policies/error-policies.routes.js'
import { groupsRouter } from './modules/groups/groups.routes.js'
import { providersRouter } from './modules/providers/providers.routes.js'
import { proxiesRouter } from './modules/proxies/proxies.routes.js'
import { settingsRouter } from './modules/settings/settings.routes.js'
import { usageRecordsRouter } from './modules/usage-records/usage-records.routes.js'
import { openAIGatewayRouter } from './modules/gateway/openai-gateway.routes.js'
import { openAIOAuthRouter } from './modules/openai-oauth/openai-oauth.routes.js'
import { runtimeConfig } from './config/runtime.js'
import { getDatabase } from './storage/database.js'

const app = express()
const port = runtimeConfig.port

getDatabase()

app.use(cors())
app.use(express.json({ limit: '2mb' }))

app.get('/health', (_req, res) => {
  res.json({ status: 'ok', service: 'juhe-ai' })
})

app.get('/api/health', (_req, res) => {
  res.json({ status: 'ok', service: 'juhe-ai' })
})

app.use('/api/providers', providersRouter)
app.use('/api/error-policies', errorPoliciesRouter)
app.use('/api/accounts', accountsRouter)
app.use('/api/groups', groupsRouter)
app.use('/api/api-keys', apiKeysRouter)
app.use('/api/openai-oauth', openAIOAuthRouter)
app.use('/api/proxies', proxiesRouter)
app.use('/api/usage-records', usageRecordsRouter)
app.use('/api/settings', settingsRouter)
app.use('/v1', openAIGatewayRouter)

app.listen(port, () => {
  console.log(`juhe-ai backend listening on http://127.0.0.1:${port}`)
})

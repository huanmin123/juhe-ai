import { existsSync } from 'node:fs'
import { resolve } from 'node:path'

import cors from 'cors'
import express from 'express'

import { accountsRouter } from './modules/accounts/accounts.routes.js'
import { requireAdmin, requireAuth } from './modules/auth/auth.middleware.js'
import { authRouter } from './modules/auth/auth.routes.js'
import { startBackgroundJobs } from './modules/background/background-jobs.js'
import { apiKeysRouter } from './modules/api-keys/api-keys.routes.js'
import { errorPoliciesRouter } from './modules/error-policies/error-policies.routes.js'
import { groupsRouter } from './modules/groups/groups.routes.js'
import { providersRouter } from './modules/providers/providers.routes.js'
import { proxiesRouter } from './modules/proxies/proxies.routes.js'
import { settingsRouter } from './modules/settings/settings.routes.js'
import { statsRouter } from './modules/stats/stats.routes.js'
import { systemAccountsRouter } from './modules/system-accounts/system-accounts.routes.js'
import { usageRecordsRouter } from './modules/usage-records/usage-records.routes.js'
import { openAIGatewayRouter } from './modules/gateway/openai-gateway.routes.js'
import { openAIOAuthRouter } from './modules/openai-oauth/openai-oauth.routes.js'
import { backendRoot, runtimeConfig } from './config/runtime.js'
import { getDatabase } from './storage/database.js'
import { listGlobalSettings } from './storage/repositories.js'
import { ok } from './shared/http.js'

const app = express()
const host = runtimeConfig.host
const port = runtimeConfig.port
const frontendDistPath = resolve(backendRoot, '..', 'frontend', 'dist')
const frontendIndexPath = resolve(frontendDistPath, 'index.html')

getDatabase()
startBackgroundJobs()

app.use(cors({ credentials: true, origin: true }))
app.use(express.json({ limit: '2mb' }))

app.get('/health', (_req, res) => {
  res.json({ status: 'ok', service: 'juhe-ai' })
})

app.get('/api/health', (_req, res) => {
	res.json({ status: 'ok', service: 'juhe-ai' })
})

app.use('/api/auth', authRouter)
app.get('/api/settings/public', (_req, res) => {
  res.json(ok(listGlobalSettings()))
})

app.use('/api', requireAuth)
app.use('/api/providers', requireAdmin, providersRouter)
app.use('/api/error-policies', errorPoliciesRouter)
app.use('/api/accounts', accountsRouter)
app.use('/api/groups', groupsRouter)
app.use('/api/api-keys', apiKeysRouter)
app.use('/api/openai-oauth', openAIOAuthRouter)
app.use('/api/proxies', requireAdmin, proxiesRouter)
app.use('/api/usage-records', usageRecordsRouter)
app.use('/api/stats', statsRouter)
app.use('/api/settings', settingsRouter)
app.use('/api/system-accounts', requireAdmin, systemAccountsRouter)
app.use('/v1', openAIGatewayRouter)

if (existsSync(frontendIndexPath)) {
  app.use(express.static(frontendDistPath))
  app.get('*', (req, res, next) => {
    if (req.path === '/health' || req.path.startsWith('/api') || req.path.startsWith('/v1')) {
      next()
      return
    }

    res.sendFile(frontendIndexPath)
  })
}

app.use((_req, res) => {
  res.status(404).json({ message: 'Not found' })
})

app.listen(port, host, () => {
  console.log(`juhe-ai backend listening on http://${host}:${port}`)
})

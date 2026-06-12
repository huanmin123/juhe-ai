import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-login-without-captcha-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'login-without-captcha-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { authRouter },
  { requestContextMiddleware },
  databaseModule
] = await Promise.all([
  import('../../modules/auth/auth.routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js')
])

const app = express()
app.set('trust proxy', true)
app.use(requestContextMiddleware)
app.use(express.json({ limit: '2mb' }))
app.use('/__aisys__/api/auth', authRouter)

try {
  const server = app.listen(0, '127.0.0.1')
  try {
    await listen(server)
    const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`
    const response = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        username: 'admin',
        password: 'admin'
      })
    })
    const text = await response.text()
    assert(response.ok, `login without captcha should succeed, got HTTP ${response.status}: ${text}`)
    assert(response.headers.get('set-cookie')?.includes('juhe_ai_session='), 'login without captcha should set a session cookie')
    console.log('Login without captcha regression passed: username/password login returns a session cookie')
  } finally {
    await closeServer(server)
  }
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('server address unavailable')
  }
  return { port: address.port }
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

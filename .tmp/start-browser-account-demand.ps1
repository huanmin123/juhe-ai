$testRoot = 'F:\sub2api-lite\.tmp\browser-account-demand'
New-Item -ItemType Directory -Force -Path $testRoot | Out-Null

$env:NODE_ENV = 'development'
$env:JUHE_AI_HOST = '127.0.0.1'
$env:JUHE_AI_PORT = '17901'
$env:JUHE_AI_DB_SERVICE_HTTP_PORT = '0'
$env:JUHE_AI_DEV_AUTO_LOGIN_USERNAME = 'admin'
$env:JUHE_AI_AUTH_CAPTCHA_DISABLED = 'true'
$env:JUHE_AI_RUNTIME_MODE = 'standalone'
$env:JUHE_AI_DATABASE_DRIVER = 'sqlite'
$env:JUHE_AI_DATABASE_PATH = Join-Path $testRoot 'business.sqlite3'
$env:JUHE_AI_CHAT_DATABASE_PATH = Join-Path $testRoot 'chat.sqlite3'
$env:JUHE_AI_DATASET_DATABASE_PATH = Join-Path $testRoot 'dataset.sqlite3'
$env:JUHE_AI_USAGE_CATALOG_DATABASE_PATH = Join-Path $testRoot 'usage-catalog.sqlite3'
$env:JUHE_AI_STATS_DATABASE_PATH = Join-Path $testRoot 'stats.sqlite3'
$env:JUHE_AI_USAGE_SHARD_ROOT = Join-Path $testRoot 'usage-shards'
$env:JUHE_AI_CODEX_CONTEXT_ROOT = Join-Path $testRoot 'codex-context'
$env:JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = Join-Path $testRoot 'codex-state'
$env:JUHE_AI_CHAT_ASSETS_ROOT = Join-Path $testRoot 'chat-assets'
$env:JUHE_AI_LOG_DIR = Join-Path $testRoot 'logs'
$env:JUHE_AI_LOG_FILE_ENABLED = 'false'
$env:JUHE_AI_SECRET = 'browser-account-demand-secret'

$backend = Start-Process -FilePath 'pnpm.cmd' `
  -ArgumentList @('run', 'dev') `
  -WorkingDirectory 'F:\sub2api-lite\backend' `
  -WindowStyle Hidden `
  -RedirectStandardOutput (Join-Path $testRoot 'backend.stdout.log') `
  -RedirectStandardError (Join-Path $testRoot 'backend.stderr.log') `
  -PassThru

$env:VITE_JUHE_AI_BACKEND_TARGET = 'http://127.0.0.1:17901'
$env:VITE_JUHE_AI_GATEWAY_BASE_URL = 'http://127.0.0.1:17901'
$frontend = Start-Process -FilePath 'pnpm.cmd' `
  -ArgumentList @('run', 'dev', '--', '--port', '17902') `
  -WorkingDirectory 'F:\sub2api-lite\frontend' `
  -WindowStyle Hidden `
  -RedirectStandardOutput (Join-Path $testRoot 'frontend.stdout.log') `
  -RedirectStandardError (Join-Path $testRoot 'frontend.stderr.log') `
  -PassThru

[pscustomobject]@{
  backendPid = $backend.Id
  frontendPid = $frontend.Id
  root = $testRoot
}

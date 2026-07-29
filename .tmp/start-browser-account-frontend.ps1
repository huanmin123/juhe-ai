$env:VITE_JUHE_AI_BACKEND_TARGET = 'http://127.0.0.1:17901'
$env:VITE_JUHE_AI_GATEWAY_BASE_URL = 'http://127.0.0.1:17901'
Start-Process -FilePath 'pnpm.cmd' `
  -ArgumentList @('exec', 'vite', '--host', '0.0.0.0', '--port', '17902') `
  -WorkingDirectory 'F:\sub2api-lite\frontend' `
  -WindowStyle Hidden `
  -RedirectStandardOutput 'F:\sub2api-lite\.tmp\browser-account-demand\frontend-17902.stdout.log' `
  -RedirectStandardError 'F:\sub2api-lite\.tmp\browser-account-demand\frontend-17902.stderr.log'

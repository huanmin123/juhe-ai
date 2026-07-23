export function resolveDevelopmentAutoLoginUsername(value) {
  return value
}

export function resolveDevelopmentBackendTarget(processEnv, frontendEnv, backendEnv) {
  const explicitTarget = firstConfiguredValue(
    processEnv.VITE_JUHE_AI_BACKEND_TARGET,
    frontendEnv.VITE_JUHE_AI_BACKEND_TARGET
  )
  if (explicitTarget) return explicitTarget

  const configuredHost = firstConfiguredValue(processEnv.JUHE_AI_HOST, backendEnv.JUHE_AI_HOST) || '127.0.0.1'
  const configuredPort = firstConfiguredValue(processEnv.JUHE_AI_PORT, backendEnv.JUHE_AI_PORT) || '3000'
  const connectHost = configuredHost === '0.0.0.0' || configuredHost === '::' ? '127.0.0.1' : configuredHost
  const urlHost = connectHost.includes(':') && !connectHost.startsWith('[') ? `[${connectHost}]` : connectHost
  return `http://${urlHost}:${configuredPort}`
}

function firstConfiguredValue(...values) {
  for (const value of values) {
    const trimmed = value?.trim()
    if (trimmed) return trimmed
  }
  return undefined
}

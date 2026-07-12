const loopbackHosts = new Set(['127.0.0.1', '::1', 'localhost'])

export function assertDevelopmentAutoLoginConfig(input: {
  username?: string
  nodeEnv?: string
  host: string
}): void {
  if (!input.username) return
  if (input.nodeEnv?.trim().toLowerCase() === 'production') {
    throw new Error('JUHE_AI_DEV_AUTO_LOGIN_USERNAME 不能在 NODE_ENV=production 时启用')
  }
  if (!loopbackHosts.has(input.host.trim().toLowerCase())) {
    throw new Error('JUHE_AI_DEV_AUTO_LOGIN_USERNAME 只允许后端监听回环地址时启用')
  }
}

export function assertDevelopmentAutoLoginConfig(input: {
  username?: string
  nodeEnv?: string
  host: string
}): void {
  if (!input.username) return
  if (input.nodeEnv?.trim().toLowerCase() === 'production') {
    throw new Error('JUHE_AI_DEV_AUTO_LOGIN_USERNAME 不能在 NODE_ENV=production 时启用')
  }
}

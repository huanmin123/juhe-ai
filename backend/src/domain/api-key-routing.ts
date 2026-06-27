export function normalizeApiKeyGroupBindingWeight(value: unknown): number {
  if (value === undefined || value === null) {
    return 1
  }
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > 100) {
    throw new Error('策略路由分组权重必须是 1-100 之间的整数')
  }
  return value
}

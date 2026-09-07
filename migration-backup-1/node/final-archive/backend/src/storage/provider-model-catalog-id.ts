import { createHash } from 'node:crypto'

export function providerModelCatalogId(providerCode: string, model: string): string {
  const slug = `${providerCode}_${model}`
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '')
    .slice(0, 72)
  const hash = createHash('sha256')
    .update(`${providerCode}\u0000${model}`)
    .digest('hex')
    .slice(0, 12)
  return `provider_model_${slug}_${hash}`
}

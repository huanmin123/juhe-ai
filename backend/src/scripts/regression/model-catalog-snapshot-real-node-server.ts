const readyPrefix = 'JUHE_AI_MODEL_CATALOG_BRIDGE_READY '

process.stdin.resume()
process.stdin.once('end', () => {
  process.emit('SIGTERM')
})

await import('../../server.js')

const port = Number(process.env.JUHE_AI_PORT)
if (!Number.isInteger(port) || port < 1 || port > 65535) {
  throw new Error('JUHE_AI_PORT is invalid')
}
process.stdout.write(`${readyPrefix}${JSON.stringify({ port })}\n`)

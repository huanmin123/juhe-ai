await import('../../storage/codex-context-state-writer-worker.js')

const secret = process.env.JUHE_AI_TEST_CODEX_WRITER_FATAL_SECRET ?? 'missing-secret'
await Promise.reject(new Error(`api_key=${secret} ${'\\"\n测'.repeat(5000)}`))

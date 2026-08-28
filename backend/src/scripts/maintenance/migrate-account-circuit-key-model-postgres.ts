import { runtimeConfig } from '../../config/runtime.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

/**
 * Additive Node-owned migration for the key_model control-plane projection.
 * Existing account/key/protocol_model rows are left untouched.
 */
async function main(): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    throw new Error('key_model PostgreSQL migration requires JUHE_AI_DATABASE_DRIVER=postgres')
  }
  const pool = await getPostgresPool()
  const client = await pool.connect()
  try {
    await client.query('BEGIN')
    await client.query('SET LOCAL lock_timeout = 5000')
    await client.query('SET LOCAL statement_timeout = 30000')
    await client.query(`
      ALTER TABLE juhe_business.account_circuit_incidents
        ADD COLUMN IF NOT EXISTS client_model text,
        ADD COLUMN IF NOT EXISTS capability_hash text,
        ADD COLUMN IF NOT EXISTS credential_source_account_id text,
        ADD COLUMN IF NOT EXISTS client_endpoint_family text,
        ADD COLUMN IF NOT EXISTS final_upstream_model text,
        ADD COLUMN IF NOT EXISTS upstream_endpoint_mode text
    `)
    await client.query(`
      ALTER TABLE juhe_business.account_circuit_incidents
        DROP CONSTRAINT IF EXISTS account_circuit_incidents_scope_kind_check,
        DROP CONSTRAINT IF EXISTS account_circuit_incidents_failure_scope_check,
        DROP CONSTRAINT IF EXISTS account_circuit_incidents_check3,
        DROP CONSTRAINT IF EXISTS account_circuit_incidents_scope_shape_check
    `)
    await client.query(`
      ALTER TABLE juhe_business.account_circuit_incidents
        ADD CONSTRAINT account_circuit_incidents_scope_kind_check
          CHECK (scope_kind IN ('account', 'key', 'protocol_model', 'key_model')),
        ADD CONSTRAINT account_circuit_incidents_failure_scope_check
          CHECK (failure_scope IS NULL OR failure_scope IN ('account', 'key', 'protocol_model', 'key_model')),
        ADD CONSTRAINT account_circuit_incidents_scope_shape_check
          CHECK (
            (scope_kind = 'account'
              AND key_fingerprint IS NULL AND protocol_code IS NULL AND request_lane IS NULL AND model_family IS NULL
              AND client_model IS NULL AND capability_hash IS NULL AND credential_source_account_id IS NULL
              AND client_endpoint_family IS NULL AND final_upstream_model IS NULL AND upstream_endpoint_mode IS NULL)
            OR (scope_kind = 'key'
              AND key_fingerprint IS NOT NULL AND protocol_code IS NULL AND request_lane IS NULL AND model_family IS NULL
              AND client_model IS NULL AND capability_hash IS NULL AND credential_source_account_id IS NULL
              AND client_endpoint_family IS NULL AND final_upstream_model IS NULL AND upstream_endpoint_mode IS NULL)
            OR (scope_kind = 'protocol_model'
              AND key_fingerprint IS NULL AND protocol_code IS NOT NULL AND request_lane IS NOT NULL AND model_family IS NOT NULL
              AND client_model IS NULL AND capability_hash IS NULL AND credential_source_account_id IS NULL
              AND client_endpoint_family IS NULL AND final_upstream_model IS NULL AND upstream_endpoint_mode IS NULL)
            OR (scope_kind = 'key_model'
              AND key_fingerprint IS NOT NULL AND capability_hash IS NOT NULL AND client_model IS NOT NULL
              AND credential_source_account_id IS NOT NULL AND client_endpoint_family IS NOT NULL
              AND final_upstream_model IS NOT NULL AND upstream_endpoint_mode IS NOT NULL
              AND protocol_code IS NULL AND request_lane IS NULL AND model_family IS NULL)
          )
    `)
    await client.query(`
      CREATE UNIQUE INDEX IF NOT EXISTS idx_account_circuit_incidents_key_model_capability
        ON juhe_business.account_circuit_incidents(scope_kind, capability_hash)
        WHERE scope_kind = 'key_model' AND capability_hash IS NOT NULL
    `)
    await client.query('COMMIT')
    console.log(JSON.stringify({ message: 'Dev PostgreSQL key_model schema migration passed' }))
  } catch (error) {
    await client.query('ROLLBACK').catch(() => undefined)
    throw error
  } finally {
    client.release()
    await closePostgresPool()
  }
}

main().catch((error: unknown) => {
  const message = error instanceof Error ? error.message : String(error)
  console.error(`Dev PostgreSQL key_model schema migration failed: ${message}`)
  process.exitCode = 1
})

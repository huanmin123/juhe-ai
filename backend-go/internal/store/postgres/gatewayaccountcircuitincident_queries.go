package postgres

const gatewayAccountCircuitIncidentProjectionColumns = `
  incident.circuit_scope_key,
  incident.account_id,
  incident.account_runtime_key,
  incident.scope_kind,
  incident.key_fingerprint,
  incident.protocol_code,
  incident.request_lane,
  incident.model_family,
  incident.incident_id,
  incident.state,
  incident.generation,
  incident.dispatch_revision,
  incident.ledger_revision,
  incident.transition_id,
  incident.open_until_ms,
  incident.next_transition_at_ms,
  incident.lease_id,
  incident.lease_purpose,
  incident.lease_until_ms,
  incident.backoff_level,
  incident.recovering_successes,
  incident.retained_until_ms,
  incident.updated_at_ms,
  account.dispatch_revision`

const loadGatewayAccountCircuitIncidentForProjectionSQL = `
SELECT ` + gatewayAccountCircuitIncidentProjectionColumns + `
FROM juhe_business.account_circuit_incidents AS incident
JOIN juhe_business.accounts AS account ON account.id = incident.account_id
WHERE incident.circuit_scope_key = $1::text`

const listGatewayAccountCircuitIncidentsForRebuildSQL = `
SELECT ` + gatewayAccountCircuitIncidentProjectionColumns + `
FROM juhe_business.account_circuit_incidents AS incident
JOIN juhe_business.accounts AS account ON account.id = incident.account_id
WHERE (incident.state <> 'CLOSED' OR incident.retained_until_ms > $1::bigint)
  AND incident.dispatch_revision = account.dispatch_revision
  AND (
    incident.updated_at_ms > $2::bigint
    OR (incident.updated_at_ms = $2::bigint AND incident.circuit_scope_key > $3::text)
  )
ORDER BY incident.updated_at_ms ASC, incident.circuit_scope_key ASC
LIMIT $4`

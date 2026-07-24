package postgres

const claimGatewayAccountCircuitOutboxSQL = `
WITH candidates AS (
  SELECT event_id, available_at_ms, created_at_ms
  FROM juhe_business.account_circuit_outbox
  WHERE projection_key = $1::text
    AND event_type IN ('dispatch_revision_changed', 'incident_changed')
    AND (
      (status = 'pending' AND available_at_ms <= $2::bigint)
      OR (status = 'processing' AND claim_until_ms <= $2::bigint)
    )
  ORDER BY available_at_ms ASC, created_at_ms ASC, event_id ASC
  LIMIT $3
  FOR UPDATE SKIP LOCKED
), claimed AS (
  UPDATE juhe_business.account_circuit_outbox AS outbox
  SET status = 'processing',
      claim_token = md5($4::text || ':' || candidates.event_id),
      claimed_by = $5::text,
      claim_until_ms = $6::bigint,
      attempt_count = outbox.attempt_count + 1,
      updated_at_ms = $2::bigint
  FROM candidates
  WHERE outbox.event_id = candidates.event_id
  RETURNING
    outbox.event_id,
    outbox.projection_key,
	  outbox.event_type,
    outbox.account_id,
    outbox.account_runtime_key,
	  outbox.circuit_scope_key,
	  outbox.incident_id,
    outbox.transition_id,
    outbox.dispatch_revision,
	  outbox.generation,
	  outbox.ledger_revision,
    outbox.claim_token,
    outbox.attempt_count,
    outbox.created_at_ms,
    candidates.available_at_ms
)
SELECT event_id, projection_key, event_type, account_id, account_runtime_key,
       circuit_scope_key, incident_id, transition_id, dispatch_revision,
       generation, ledger_revision, claim_token, attempt_count, created_at_ms
FROM claimed
ORDER BY available_at_ms ASC, created_at_ms ASC, event_id ASC`

const lockGatewayAccountCircuitOutboxForAcknowledgeSQL = `
SELECT projection_key, event_type, account_id, dispatch_revision, circuit_scope_key,
       incident_id, ledger_revision, status, claim_token
FROM juhe_business.account_circuit_outbox
WHERE event_id = $1::text
FOR UPDATE`

const acknowledgeGatewayAccountCircuitOutboxSQL = `
UPDATE juhe_business.account_circuit_outbox
SET status = 'dispatched',
    claim_token = NULL,
    claimed_by = NULL,
    claim_until_ms = NULL,
    acknowledged_at_ms = $4::bigint,
    last_error_class = NULL,
    updated_at_ms = $4::bigint
WHERE event_id = $1::text
  AND projection_key = $2::text
  AND status = 'processing'
  AND claim_token = $3::text`

const advanceGatewayAccountCircuitProjectionRevisionSQL = `
UPDATE juhe_business.accounts
SET circuit_projection_revision = GREATEST(circuit_projection_revision, $2::bigint)
WHERE id = $1::text
  AND dispatch_revision >= $2::bigint`

const advanceGatewayAccountCircuitIncidentProjectionRevisionSQL = `
UPDATE juhe_business.account_circuit_incidents
SET projected_ledger_revision = GREATEST(projected_ledger_revision, $3::bigint)
WHERE circuit_scope_key = $1::text
  AND incident_id = $2::text
  AND ledger_revision >= $3::bigint`

const releaseGatewayAccountCircuitOutboxSQL = `
UPDATE juhe_business.account_circuit_outbox
SET status = 'pending',
    available_at_ms = $4::bigint,
    claim_token = NULL,
    claimed_by = NULL,
    claim_until_ms = NULL,
    last_error_class = $3::text,
    updated_at_ms = $5::bigint
WHERE event_id = $1::text
  AND status = 'processing'
  AND claim_token = $2::text`

package postgres

const listGatewayAccountCircuitDispatchRevisionsSQL = `
SELECT id, dispatch_revision
FROM juhe_business.accounts
WHERE id > $1::text
  AND dispatch_revision >= 1
ORDER BY id ASC
LIMIT $2`

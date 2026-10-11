-- name: UpsertNode :exec
INSERT INTO nodes(id, name, host, port, server_name, public_key, short_id, config_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name, host = EXCLUDED.host, port = EXCLUDED.port,
    server_name = EXCLUDED.server_name, public_key = EXCLUDED.public_key, short_id = EXCLUDED.short_id, config_hash = EXCLUDED.config_hash,
    revision = nodes.revision + CASE WHEN
        (nodes.host, nodes.port, nodes.server_name, nodes.public_key, nodes.short_id, nodes.config_hash)
        IS DISTINCT FROM (EXCLUDED.host, EXCLUDED.port, EXCLUDED.server_name, EXCLUDED.public_key, EXCLUDED.short_id, EXCLUDED.config_hash)
        THEN 1 ELSE 0 END;

-- name: ListNodes :many
SELECT * FROM nodes ORDER BY name, id;

-- name: GetNode :one
SELECT * FROM nodes WHERE id=$1;

-- name: InsertDevice :one
INSERT INTO devices(id, node_id, name, credential_ciphertext) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: ListDevices :many
SELECT * FROM devices ORDER BY created_at, id;

-- name: GetDevice :one
SELECT * FROM devices WHERE id=$1;

-- name: RevokeDevice :one
UPDATE devices SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL RETURNING *;

-- name: ActiveClients :many
SELECT * FROM devices WHERE node_id=$1 AND revoked_at IS NULL ORDER BY id;

-- name: AdvanceRevision :exec
UPDATE nodes SET revision=revision+1 WHERE id=$1;

-- name: MarkApplied :exec
UPDATE nodes SET applied_revision=revision WHERE id=$1;

-- name: InsertAudit :exec
INSERT INTO audit_events(action, device_id) VALUES ($1, $2);

-- name: ListAudit :many
SELECT * FROM audit_events ORDER BY id DESC LIMIT 100;

-- name: CountDevices :one
SELECT count(*) AS total, count(*) FILTER (WHERE revoked_at IS NULL) AS active FROM devices WHERE node_id=$1;

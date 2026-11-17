-- name: UpsertNode :exec
INSERT INTO nodes(id, name, host, port, server_name, public_key, short_id, config_hash, xhttp_config, h3_config)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name, host = EXCLUDED.host, port = EXCLUDED.port,
    server_name = EXCLUDED.server_name, public_key = EXCLUDED.public_key, short_id = EXCLUDED.short_id, config_hash = EXCLUDED.config_hash,
    connection_mode = CASE WHEN nodes.connection_mode='bridge' AND
        (nodes.host,nodes.port,nodes.xhttp_config->>'port') IS DISTINCT FROM
        (EXCLUDED.host,EXCLUDED.port,EXCLUDED.xhttp_config->>'port') THEN 'unconfigured' ELSE nodes.connection_mode END,
    bridge_config = CASE WHEN nodes.connection_mode='bridge' AND
        (nodes.host,nodes.port,nodes.xhttp_config->>'port') IS DISTINCT FROM
        (EXCLUDED.host,EXCLUDED.port,EXCLUDED.xhttp_config->>'port') THEN NULL ELSE nodes.bridge_config END,
    xhttp_config = EXCLUDED.xhttp_config, h3_config = EXCLUDED.h3_config,
    revision = nodes.revision + CASE WHEN
        (nodes.host, nodes.port, nodes.server_name, nodes.public_key, nodes.short_id, nodes.config_hash, nodes.xhttp_config, nodes.h3_config)
        IS DISTINCT FROM (EXCLUDED.host, EXCLUDED.port, EXCLUDED.server_name, EXCLUDED.public_key, EXCLUDED.short_id, EXCLUDED.config_hash, EXCLUDED.xhttp_config, EXCLUDED.h3_config)
        THEN 1 ELSE 0 END;

-- name: ListNodes :many
SELECT * FROM nodes ORDER BY name, id;

-- name: GetNode :one
SELECT * FROM nodes WHERE id=$1;

-- name: InsertRemoteNode :one
INSERT INTO nodes(id, name, host, port, server_name, public_key, short_id, config_hash,
                  xhttp_config, h3_config, kind, agent_join_hash, agent_join_expires_at, revision)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'remote', $11, $12, 1)
RETURNING *;

-- name: UpdateRemoteNode :one
UPDATE nodes SET name=$2, host=$3, port=$4, server_name=$5, public_key=$6,
                 connection_mode=CASE WHEN connection_mode='bridge' AND
                     (host,port,xhttp_config->>'port') IS DISTINCT FROM ($3,$4,$8::jsonb->>'port') THEN 'unconfigured' ELSE connection_mode END,
                 bridge_config=CASE WHEN connection_mode='bridge' AND
                     (host,port,xhttp_config->>'port') IS DISTINCT FROM ($3,$4,$8::jsonb->>'port') THEN NULL ELSE bridge_config END,
                 short_id=$7, xhttp_config=$8, h3_config=$9,
                 revision=revision+1
WHERE id=$1 AND kind='remote' RETURNING *;

-- name: DeleteRemoteNode :execrows
DELETE FROM nodes WHERE nodes.id=$1 AND kind='remote'
  AND NOT EXISTS (SELECT 1 FROM devices WHERE node_id=$1);

-- name: SetNodeConnection :one
UPDATE nodes SET connection_mode=$2, bridge_config=$3 WHERE id=$1 RETURNING *;

-- name: SetRequestedRevision :one
UPDATE nodes SET requested_revision=revision WHERE id=$1 AND kind='remote' RETURNING *;

-- name: EnrollAgent :one
UPDATE nodes SET agent_token_hash=$3, agent_join_hash=NULL, agent_join_expires_at=NULL,
                 agent_last_seen_at=now(), agent_error=''
WHERE id=$1 AND kind='remote' AND agent_join_hash=$2 AND agent_join_expires_at>now()
  AND agent_token_hash IS NULL RETURNING *;

-- name: RotateAgentJoin :one
UPDATE nodes SET agent_join_hash=$2, agent_join_expires_at=$3,
                 agent_token_hash=NULL, agent_last_seen_at=NULL,
                 agent_error='awaiting enrollment', applied_revision=0
WHERE id=$1 AND kind='remote' RETURNING *;

-- name: GetAgentNode :one
SELECT * FROM nodes WHERE id=$1 AND kind='remote' AND agent_token_hash=$2;

-- name: TouchAgent :exec
UPDATE nodes SET agent_last_seen_at=now() WHERE id=$1 AND agent_token_hash=$2;

-- name: AgentAck :execrows
UPDATE nodes SET applied_revision=$2, agent_last_seen_at=now(), agent_error=''
WHERE id=$1 AND kind='remote' AND agent_token_hash=$3 AND revision=$2
  AND requested_revision=$2 AND applied_revision<$2;

-- name: AgentFailure :execrows
UPDATE nodes SET agent_last_seen_at=now(), agent_error=$3
WHERE id=$1 AND kind='remote' AND agent_token_hash=$2
  AND revision=$4 AND requested_revision=$4;

-- name: InsertDevice :one
INSERT INTO devices(id, node_id, name, credential_ciphertext) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: ListDevices :many
SELECT * FROM devices ORDER BY created_at, id;

-- name: GetDevice :one
SELECT * FROM devices WHERE id=$1;

-- name: UpdateDeviceMetadata :one
UPDATE devices SET name=$2, preferred_exit=$3 WHERE id=$1 RETURNING *;

-- name: RevokeDevice :one
UPDATE devices SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL RETURNING *;

-- name: DeleteRevokedDevice :execrows
DELETE FROM devices WHERE id=$1 AND revoked_at IS NOT NULL;

-- name: SetSubscriptionHash :exec
UPDATE devices SET subscription_hash=$2 WHERE id=$1;

-- name: AdvanceSubscription :one
UPDATE devices SET subscription_generation=subscription_generation+1, subscription_hash=NULL
WHERE id=$1 AND revoked_at IS NULL RETURNING *;

-- name: GetActiveDeviceBySubscription :one
SELECT * FROM devices WHERE subscription_hash=$1 AND revoked_at IS NULL;

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

-- name: CountAllDevices :one
SELECT count(*) AS total, count(*) FILTER (WHERE revoked_at IS NULL) AS active FROM devices;

-- name: GetAdministrator :one
SELECT * FROM administrator_credentials WHERE id=1;

-- name: ImportAdministrator :exec
INSERT INTO administrator_credentials (id, username, password_hash)
VALUES (1, $1, $2) ON CONFLICT (id) DO NOTHING;

-- name: SetAdministrator :exec
INSERT INTO administrator_credentials (id, username, password_hash)
VALUES (1, $1, $2)
ON CONFLICT (id) DO UPDATE SET username=EXCLUDED.username,
    password_hash=EXCLUDED.password_hash, totp_secret_ciphertext=NULL,
    totp_last_step=NULL, generation=administrator_credentials.generation+1,
    updated_at=now();

-- name: EnableAdministratorTOTP :execrows
UPDATE administrator_credentials SET totp_secret_ciphertext=$1, totp_last_step=$2,
    updated_at=now() WHERE id=1 AND generation=$3 AND totp_secret_ciphertext IS NULL;

-- name: UseAdministratorTOTP :execrows
UPDATE administrator_credentials SET totp_last_step=$1 WHERE id=1 AND generation=$2
    AND totp_secret_ciphertext IS NOT NULL AND totp_last_step < $1;

-- name: RestoreAdministrator :execrows
UPDATE administrator_credentials SET username=$1, password_hash=$2,
    totp_secret_ciphertext=$3, totp_last_step=$4, generation=generation+1,
    updated_at=now() WHERE id=1 AND generation=$5;

-- name: DeleteAdministrator :execrows
DELETE FROM administrator_credentials WHERE id=1 AND generation=$1;

CREATE TABLE nodes (
    id uuid PRIMARY KEY,
    name text NOT NULL,
    host text NOT NULL,
    port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
    server_name text NOT NULL,
    public_key text NOT NULL,
    short_id text NOT NULL,
    config_hash text NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    applied_revision bigint NOT NULL DEFAULT 0
);
CREATE TABLE devices (
    id uuid PRIMARY KEY,
    node_id uuid NOT NULL REFERENCES nodes(id),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    credential_ciphertext bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);
CREATE UNIQUE INDEX devices_active_name ON devices(node_id, name) WHERE revoked_at IS NULL;
CREATE INDEX devices_node ON devices(node_id, created_at);
CREATE TABLE audit_events (
    id bigserial PRIMARY KEY,
    action text NOT NULL,
    device_id uuid REFERENCES devices(id),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION protect_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit events are append-only';
END;
$$;
CREATE TRIGGER audit_immutable BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION protect_audit();

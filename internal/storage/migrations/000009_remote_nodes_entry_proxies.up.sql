-- Входной SOCKS используется клиентом до VLESS и не меняет выход Xray на узле.
CREATE TABLE entry_proxies (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 64),
    host text NOT NULL,
    port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
    auth_type text NOT NULL CHECK (auth_type IN ('none', 'password')),
    username_ciphertext bytea,
    password_ciphertext bytea,
    enabled boolean NOT NULL DEFAULT true,
    supports_udp boolean NOT NULL DEFAULT false,
    CHECK ((auth_type = 'none' AND username_ciphertext IS NULL AND password_ciphertext IS NULL)
        OR (auth_type = 'password' AND username_ciphertext IS NOT NULL AND password_ciphertext IS NOT NULL))
);

ALTER TABLE nodes
    ADD COLUMN kind text NOT NULL DEFAULT 'local' CHECK (kind IN ('local', 'remote')),
    ADD COLUMN entry_proxy_id uuid REFERENCES entry_proxies(id) ON DELETE RESTRICT,
    ADD COLUMN agent_join_hash bytea,
    ADD COLUMN agent_join_expires_at timestamptz,
    ADD COLUMN agent_token_hash bytea,
    ADD COLUMN agent_last_seen_at timestamptz,
    ADD COLUMN agent_error text NOT NULL DEFAULT '',
    ADD COLUMN requested_revision bigint NOT NULL DEFAULT 0,
    ADD COLUMN h3_config jsonb;

ALTER TABLE nodes ADD CONSTRAINT nodes_h3_config_object
    CHECK (h3_config IS NULL OR jsonb_typeof(h3_config) = 'object');

ALTER TABLE devices
    ADD COLUMN chain_subscription_generation bigint NOT NULL DEFAULT 0,
    ADD COLUMN chain_subscription_hash bytea UNIQUE;

-- Старое предпочтение выхода через серверный прокси больше не применяется.
UPDATE devices SET preferred_exit='server' WHERE preferred_exit='proxy';

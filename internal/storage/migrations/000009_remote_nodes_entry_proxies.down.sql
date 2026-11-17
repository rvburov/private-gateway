ALTER TABLE devices
    DROP COLUMN chain_subscription_hash,
    DROP COLUMN chain_subscription_generation;

ALTER TABLE nodes
    DROP CONSTRAINT nodes_h3_config_object,
    DROP COLUMN h3_config,
    DROP COLUMN requested_revision,
    DROP COLUMN agent_error,
    DROP COLUMN agent_last_seen_at,
    DROP COLUMN agent_token_hash,
    DROP COLUMN agent_join_expires_at,
    DROP COLUMN agent_join_hash,
    DROP COLUMN entry_proxy_id,
    DROP COLUMN kind;

DROP TABLE entry_proxies;

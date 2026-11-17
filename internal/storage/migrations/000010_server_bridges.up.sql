-- Клиентская SOCKS5-цепочка удалена. Прежнее назначение требует явного выбора
-- нового маршрута: обычная подписка не должна незаметно стать прямой.
ALTER TABLE nodes
    ADD COLUMN connection_mode text NOT NULL DEFAULT 'direct'
        CHECK (connection_mode IN ('direct', 'bridge', 'unconfigured')),
    ADD COLUMN bridge_config jsonb;
UPDATE nodes SET connection_mode='unconfigured' WHERE entry_proxy_id IS NOT NULL;
ALTER TABLE nodes ADD CONSTRAINT nodes_bridge_config_valid CHECK (
    (connection_mode='bridge' AND bridge_config IS NOT NULL AND jsonb_typeof(bridge_config)='object')
    OR (connection_mode IN ('direct', 'unconfigured') AND bridge_config IS NULL)
);

-- Устройства, обычные UUID и подписки сохраняются. Специальные токены цепочки
-- и шифротексты входных прокси удаляются, а их HTTP-пути отвечают 410.
ALTER TABLE devices DROP COLUMN chain_subscription_hash, DROP COLUMN chain_subscription_generation;
ALTER TABLE nodes DROP COLUMN entry_proxy_id;
DROP TABLE entry_proxies;

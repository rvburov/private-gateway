-- Ссылка-подписка устройства выводится из главного ключа; база хранит только SHA-256 её токена.
-- Новое поколение заменяет ссылку.
ALTER TABLE devices ADD COLUMN subscription_generation bigint NOT NULL DEFAULT 1;
ALTER TABLE devices ADD COLUMN subscription_hash bytea;
CREATE UNIQUE INDEX devices_subscription_hash ON devices(subscription_hash);

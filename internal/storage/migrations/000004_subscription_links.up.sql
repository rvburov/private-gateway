-- A device's subscription link is derived from the master key; the database keeps only the
-- SHA-256 hash of its token. A new generation replaces the link.
ALTER TABLE devices ADD COLUMN subscription_generation bigint NOT NULL DEFAULT 1;
ALTER TABLE devices ADD COLUMN subscription_hash bytea;
CREATE UNIQUE INDEX devices_subscription_hash ON devices(subscription_hash);

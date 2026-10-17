DROP INDEX devices_subscription_hash;
ALTER TABLE devices DROP COLUMN subscription_hash;
ALTER TABLE devices DROP COLUMN subscription_generation;

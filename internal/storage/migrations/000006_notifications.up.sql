-- Центр уведомлений и подписки администратора; секреты push зашифрованы главным ключом.
CREATE TABLE notification_preferences (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    server boolean NOT NULL DEFAULT true,
    devices boolean NOT NULL DEFAULT true,
    configuration boolean NOT NULL DEFAULT true
);
CREATE TABLE notification_keys (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    ciphertext bytea NOT NULL
);
CREATE TABLE notifications (
    id uuid PRIMARY KEY,
    category text NOT NULL CHECK (category IN ('server', 'devices', 'configuration')),
    title text NOT NULL,
    body text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    read_at timestamptz
);
CREATE INDEX notifications_created_at ON notifications(created_at DESC);
CREATE TABLE notification_push_subscriptions (
    id uuid PRIMARY KEY,
    endpoint_hash text NOT NULL UNIQUE,
    ciphertext bytea NOT NULL,
    device text NOT NULL,
    identity_hash bytea NOT NULL CHECK (octet_length(identity_hash) = 32),
    generation bigint NOT NULL,
    remembered boolean NOT NULL DEFAULT false,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_sent_at timestamptz
);

-- Запомненный вход хранит только хеш случайного ключа; смена администратора удаляет записи.
CREATE TABLE administrator_remembered_logins (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    administrator_id integer NOT NULL REFERENCES administrator_credentials(id) ON DELETE CASCADE,
    generation bigint NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX administrator_remembered_logins_expires_at ON administrator_remembered_logins(expires_at);

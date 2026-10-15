CREATE TABLE administrator_credentials (
    id integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    username text NOT NULL CHECK (char_length(username) BETWEEN 3 AND 64),
    password_hash text NOT NULL,
    totp_secret_ciphertext bytea,
    totp_last_step bigint,
    generation bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((totp_secret_ciphertext IS NULL) = (totp_last_step IS NULL))
);

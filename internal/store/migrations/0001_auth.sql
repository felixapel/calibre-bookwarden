-- The single admin account. The unique index on a constant allows one row only.
CREATE TABLE users (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username      text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_single_admin ON users ((true));

-- Browser sessions. Only the SHA-256 of the session token is stored.
CREATE TABLE sessions (
    token_hash   bytea PRIMARY KEY,
    user_id      bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    csrf_token   text NOT NULL,
    created_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL
);
CREATE INDEX sessions_expires_at ON sessions (expires_at);
CREATE INDEX sessions_last_seen_at ON sessions (last_seen_at);

-- The current one-time setup code (hashed); the row is deleted by setup.
CREATE TABLE setup (
    id         boolean PRIMARY KEY DEFAULT true CHECK (id),
    code_hash  bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Replay-protection nonce ledger for protocol v1.
--
-- (credential_id, nonce) is a replay identity distinct from the idempotency
-- identity. A nonce is retained until its computed expiry (at least 600 seconds
-- after first observation and after its signed timestamp). This table is
-- additive and does not alter any existing durability semantics.

CREATE TABLE nonce_ledger (
    credential_id text NOT NULL,
    nonce text NOT NULL,
    signed_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (credential_id, nonce),
    CONSTRAINT nonce_ledger_lengths
        CHECK (
            length(credential_id) BETWEEN 1 AND 255
            AND length(nonce) BETWEEN 1 AND 512
        ),
    CONSTRAINT nonce_ledger_window
        CHECK (expires_at > created_at)
);

CREATE INDEX nonce_ledger_expiry ON nonce_ledger (expires_at);

CREATE TYPE hookship_idempotency_state AS ENUM (
    'pending',
    'committed',
    'tombstone'
);

CREATE TYPE hookship_payload_backend AS ENUM (
    'postgres',
    'object_store'
);

CREATE TYPE hookship_payload_state AS ENUM (
    'staging',
    'finalized',
    'committed',
    'orphaned',
    'deleting',
    'deleted'
);

CREATE TYPE hookship_stage_write_state AS ENUM (
    'ready',
    'leased',
    'promoted',
    'orphaned'
);

CREATE TYPE hookship_delivery_state AS ENUM (
    'ready',
    'running',
    'succeeded',
    'failed',
    'dead'
);

CREATE TYPE hookship_work_state AS ENUM (
    'ready',
    'leased',
    'retry',
    'succeeded',
    'published',
    'dead'
);

CREATE TABLE ingest_idempotency_ledgers (
    ledger_id text PRIMARY KEY,
    assignment_id text NOT NULL,
    idempotency_key text NOT NULL,
    request_fingerprint bytea NOT NULL,
    body_sha256 bytea NOT NULL,
    event_id text NOT NULL,
    event_type text NOT NULL,
    route text NOT NULL,
    content_type text NOT NULL,
    state hookship_idempotency_state NOT NULL DEFAULT 'pending',
    ingest_id text,
    payload_id text,
    dispatch_job_id text,
    reserved_at timestamptz NOT NULL,
    accepted_at timestamptz,
    idempotency_expires_at timestamptz,
    tombstone_expires_at timestamptz,
    updated_at timestamptz NOT NULL,
    CONSTRAINT ingest_idempotency_ledgers_assignment_id_length
        CHECK (length(assignment_id) BETWEEN 1 AND 255),
    CONSTRAINT ingest_idempotency_ledgers_key_length
        CHECK (length(idempotency_key) BETWEEN 1 AND 255),
    CONSTRAINT ingest_idempotency_ledgers_ledger_id_length
        CHECK (length(ledger_id) BETWEEN 1 AND 255),
    CONSTRAINT ingest_idempotency_ledgers_hash_lengths
        CHECK (octet_length(request_fingerprint) = 32 AND octet_length(body_sha256) = 32),
    CONSTRAINT ingest_idempotency_ledgers_header_lengths
        CHECK (
            length(event_id) BETWEEN 1 AND 255
            AND length(event_type) BETWEEN 1 AND 255
            AND length(route) BETWEEN 1 AND 255
            AND length(content_type) BETWEEN 1 AND 255
        ),
    CONSTRAINT ingest_idempotency_ledgers_state_shape
        CHECK (
            (
                state = 'pending'
                AND ingest_id IS NULL
                AND payload_id IS NULL
                AND dispatch_job_id IS NULL
                AND accepted_at IS NULL
                AND idempotency_expires_at IS NULL
                AND tombstone_expires_at IS NULL
            )
            OR (
                state IN ('committed', 'tombstone')
                AND ingest_id IS NOT NULL
                AND payload_id IS NOT NULL
                AND dispatch_job_id IS NOT NULL
                AND accepted_at IS NOT NULL
                AND idempotency_expires_at >= accepted_at + interval '24 hours'
                AND tombstone_expires_at >= idempotency_expires_at + interval '24 hours'
            )
        ),
    UNIQUE (assignment_id, ledger_id),
    UNIQUE (assignment_id, idempotency_key)
);

CREATE TABLE event_ingests (
    ingest_id text PRIMARY KEY,
    assignment_id text NOT NULL,
    ledger_id text NOT NULL,
    request_fingerprint bytea NOT NULL,
    body_sha256 bytea NOT NULL,
    event_id text NOT NULL,
    event_type text NOT NULL,
    route text NOT NULL,
    content_type text NOT NULL,
    accepted_at timestamptz NOT NULL,
    idempotency_expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT event_ingests_id_lengths
        CHECK (
            length(ingest_id) BETWEEN 1 AND 255
            AND length(assignment_id) BETWEEN 1 AND 255
            AND length(ledger_id) BETWEEN 1 AND 255
        ),
    CONSTRAINT event_ingests_hash_lengths
        CHECK (octet_length(request_fingerprint) = 32 AND octet_length(body_sha256) = 32),
    CONSTRAINT event_ingests_header_lengths
        CHECK (
            length(event_id) BETWEEN 1 AND 255
            AND length(event_type) BETWEEN 1 AND 255
            AND length(route) BETWEEN 1 AND 255
            AND length(content_type) BETWEEN 1 AND 255
        ),
    CONSTRAINT event_ingests_retry_horizon
        CHECK (idempotency_expires_at >= accepted_at + interval '24 hours'),
    UNIQUE (assignment_id, ingest_id),
    UNIQUE (assignment_id, ledger_id),
    FOREIGN KEY (assignment_id, ledger_id)
        REFERENCES ingest_idempotency_ledgers (assignment_id, ledger_id)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE payload_records (
    payload_id text PRIMARY KEY,
    assignment_id text NOT NULL,
    ledger_id text NOT NULL,
    ingest_id text,
    backend hookship_payload_backend NOT NULL,
    state hookship_payload_state NOT NULL,
    ciphertext bytea,
    key_reference text NOT NULL,
    key_version text NOT NULL,
    nonce bytea NOT NULL,
    ciphertext_sha256 bytea NOT NULL,
    ciphertext_size bigint NOT NULL,
    staging_object_key text,
    final_object_key text,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    staged_at timestamptz,
    finalized_at timestamptz,
    committed_at timestamptz,
    deletion_started_at timestamptz,
    deletion_lease_owner text,
    deletion_lease_token text,
    deletion_lease_expires_at timestamptz,
    deletion_attempts integer NOT NULL DEFAULT 0,
    deleted_at timestamptz,
    CONSTRAINT payload_records_id_lengths
        CHECK (
            length(payload_id) BETWEEN 1 AND 255
            AND length(assignment_id) BETWEEN 1 AND 255
            AND length(ledger_id) BETWEEN 1 AND 255
            AND length(key_reference) BETWEEN 1 AND 512
            AND length(key_version) BETWEEN 1 AND 128
            AND (deletion_lease_owner IS NULL OR length(deletion_lease_owner) BETWEEN 1 AND 255)
            AND (deletion_lease_token IS NULL OR length(deletion_lease_token) BETWEEN 1 AND 255)
        ),
    CONSTRAINT payload_records_crypto_shape
        CHECK (
            octet_length(nonce) BETWEEN 12 AND 32
            AND octet_length(ciphertext_sha256) = 32
            AND ciphertext_size > 0
        ),
    CONSTRAINT payload_records_ttl
        CHECK (expires_at > created_at),
    CONSTRAINT payload_records_postgres_shape
        CHECK (
            backend <> 'postgres'
            OR (
                state IN ('committed', 'deleting', 'deleted')
                AND ingest_id IS NOT NULL
                AND staging_object_key IS NULL
                AND final_object_key IS NULL
                AND ciphertext_size <= 16777216
                AND (
                    (state <> 'deleted' AND ciphertext IS NOT NULL AND octet_length(ciphertext) = ciphertext_size)
                    OR (state = 'deleted' AND ciphertext IS NULL)
                )
            )
        ),
    CONSTRAINT payload_records_object_shape
        CHECK (
            backend <> 'object_store'
            OR (
                ciphertext IS NULL
                AND staging_object_key IS NOT NULL
                AND final_object_key IS NOT NULL
                AND length(staging_object_key) <= 1024
                AND length(final_object_key) <= 1024
                AND expires_at >= created_at + interval '24 hours'
            )
        ),
    CONSTRAINT payload_records_state_timestamps
        CHECK (
            (
                state = 'staging'
                AND ingest_id IS NULL
                AND staged_at IS NOT NULL
                AND finalized_at IS NULL
                AND committed_at IS NULL
                AND deletion_started_at IS NULL
                AND deletion_lease_owner IS NULL
                AND deletion_lease_token IS NULL
                AND deletion_lease_expires_at IS NULL
                AND deletion_attempts = 0
                AND deleted_at IS NULL
            )
            OR (
                state = 'finalized'
                AND ingest_id IS NULL
                AND staged_at IS NOT NULL
                AND finalized_at IS NOT NULL
                AND committed_at IS NULL
                AND deletion_started_at IS NULL
                AND deletion_lease_owner IS NULL
                AND deletion_lease_token IS NULL
                AND deletion_lease_expires_at IS NULL
                AND deletion_attempts = 0
                AND deleted_at IS NULL
            )
            OR (
                state = 'committed'
                AND ingest_id IS NOT NULL
                AND committed_at IS NOT NULL
                AND deletion_started_at IS NULL
                AND deletion_lease_owner IS NULL
                AND deletion_lease_token IS NULL
                AND deletion_lease_expires_at IS NULL
                AND deletion_attempts = 0
                AND deleted_at IS NULL
            )
            OR (
                state = 'orphaned'
                AND backend = 'object_store'
                AND ingest_id IS NULL
                AND deletion_started_at IS NULL
                AND deletion_lease_owner IS NULL
                AND deletion_lease_token IS NULL
                AND deletion_lease_expires_at IS NULL
                AND deletion_attempts = 0
                AND deleted_at IS NULL
            )
            OR (
                state = 'deleting'
                AND deletion_started_at IS NOT NULL
                AND deletion_lease_owner IS NOT NULL
                AND deletion_lease_token IS NOT NULL
                AND deletion_lease_expires_at IS NOT NULL
                AND deletion_attempts > 0
                AND deleted_at IS NULL
            )
            OR (
                state = 'deleted'
                AND deletion_started_at IS NOT NULL
                AND deletion_lease_owner IS NULL
                AND deletion_lease_token IS NULL
                AND deletion_lease_expires_at IS NULL
                AND deletion_attempts > 0
                AND deleted_at IS NOT NULL
            )
        ),
    UNIQUE (assignment_id, payload_id),
    UNIQUE (assignment_id, payload_id, ledger_id),
    UNIQUE (assignment_id, ledger_id),
    UNIQUE (assignment_id, final_object_key),
    FOREIGN KEY (assignment_id, ledger_id)
        REFERENCES ingest_idempotency_ledgers (assignment_id, ledger_id)
        DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (assignment_id, ingest_id)
        REFERENCES event_ingests (assignment_id, ingest_id)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE payload_stage_write_attempts (
    stage_write_attempt_id text PRIMARY KEY,
    assignment_id text NOT NULL,
    payload_id text NOT NULL,
    ledger_id text NOT NULL,
    attempt_number integer NOT NULL,
    state hookship_stage_write_state NOT NULL,
    staging_object_key text NOT NULL,
    final_object_key text NOT NULL,
    ciphertext_sha256 bytea NOT NULL,
    ciphertext_size bigint NOT NULL,
    lease_owner text,
    lease_token text,
    lease_expires_at timestamptz,
    provider_staging_version text,
    provider_final_version text,
    promotion_mode text,
    expected_final_version text,
    created_at timestamptz NOT NULL,
    promoted_at timestamptz,
    orphaned_at timestamptz,
    CONSTRAINT payload_stage_write_attempts_id_lengths
        CHECK (
            length(stage_write_attempt_id) BETWEEN 1 AND 255
            AND length(assignment_id) BETWEEN 1 AND 255
            AND length(payload_id) BETWEEN 1 AND 255
            AND length(ledger_id) BETWEEN 1 AND 255
            AND length(staging_object_key) BETWEEN 1 AND 1024
            AND length(final_object_key) BETWEEN 1 AND 1024
            AND (lease_owner IS NULL OR length(lease_owner) BETWEEN 1 AND 255)
            AND (lease_token IS NULL OR length(lease_token) BETWEEN 1 AND 255)
            AND (provider_staging_version IS NULL OR length(provider_staging_version) BETWEEN 1 AND 512)
            AND (provider_final_version IS NULL OR length(provider_final_version) BETWEEN 1 AND 512)
            AND (expected_final_version IS NULL OR length(expected_final_version) BETWEEN 1 AND 512)
        ),
    CONSTRAINT payload_stage_write_attempts_crypto_shape
        CHECK (
            octet_length(ciphertext_sha256) = 32
            AND ciphertext_size > 0
            AND attempt_number > 0
        ),
    CONSTRAINT payload_stage_write_attempts_state_shape
        CHECK (
            (
                state = 'ready'
                AND lease_owner IS NULL
                AND lease_token IS NULL
                AND lease_expires_at IS NULL
                AND provider_staging_version IS NULL
                AND provider_final_version IS NULL
                AND promotion_mode IS NULL
                AND expected_final_version IS NULL
                AND promoted_at IS NULL
                AND orphaned_at IS NULL
            )
            OR (
                state = 'leased'
                AND lease_owner IS NOT NULL
                AND lease_token IS NOT NULL
                AND lease_expires_at IS NOT NULL
                AND provider_staging_version IS NULL
                AND provider_final_version IS NULL
                AND promotion_mode IS NULL
                AND expected_final_version IS NULL
                AND promoted_at IS NULL
                AND orphaned_at IS NULL
            )
            OR (
                state = 'promoted'
                AND lease_owner IS NULL
                AND lease_token IS NULL
                AND lease_expires_at IS NULL
                AND provider_staging_version IS NOT NULL
                AND provider_final_version IS NOT NULL
                AND promotion_mode IN ('create_only', 'match_version', 'verified_existing')
                AND (
                    (promotion_mode = 'match_version' AND expected_final_version IS NOT NULL)
                    OR (promotion_mode <> 'match_version' AND expected_final_version IS NULL)
                )
                AND promoted_at IS NOT NULL
                AND orphaned_at IS NULL
            )
            OR (
                state = 'orphaned'
                AND lease_owner IS NULL
                AND lease_token IS NULL
                AND lease_expires_at IS NULL
                AND provider_staging_version IS NULL
                AND provider_final_version IS NULL
                AND promotion_mode IS NULL
                AND expected_final_version IS NULL
                AND promoted_at IS NULL
                AND orphaned_at IS NOT NULL
            )
        ),
    UNIQUE (assignment_id, stage_write_attempt_id),
    UNIQUE (assignment_id, payload_id, attempt_number),
    UNIQUE (assignment_id, staging_object_key),
    UNIQUE (assignment_id, final_object_key),
    FOREIGN KEY (assignment_id, payload_id, ledger_id)
        REFERENCES payload_records (assignment_id, payload_id, ledger_id)
);

CREATE TABLE endpoint_snapshots (
    endpoint_snapshot_id text PRIMARY KEY,
    assignment_id text NOT NULL,
    endpoint_id text NOT NULL,
    bundle_id text NOT NULL,
    config_generation bigint NOT NULL,
    content bytea NOT NULL,
    content_sha256 bytea NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT endpoint_snapshots_id_lengths
        CHECK (
            length(endpoint_snapshot_id) BETWEEN 1 AND 255
            AND length(assignment_id) BETWEEN 1 AND 255
            AND length(endpoint_id) BETWEEN 1 AND 255
            AND length(bundle_id) BETWEEN 1 AND 255
        ),
    CONSTRAINT endpoint_snapshots_content_shape
        CHECK (octet_length(content) BETWEEN 2 AND 1048576 AND octet_length(content_sha256) = 32),
    CONSTRAINT endpoint_snapshots_generation
        CHECK (config_generation BETWEEN 1 AND 9007199254740991),
    UNIQUE (assignment_id, endpoint_snapshot_id),
    UNIQUE (assignment_id, endpoint_id, config_generation)
);

CREATE TABLE deliveries (
    delivery_id text PRIMARY KEY,
    assignment_id text NOT NULL,
    ingest_id text NOT NULL,
    payload_id text NOT NULL,
    endpoint_snapshot_id text NOT NULL,
    state hookship_delivery_state NOT NULL DEFAULT 'ready',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT deliveries_id_lengths
        CHECK (
            length(delivery_id) BETWEEN 1 AND 255
            AND length(assignment_id) BETWEEN 1 AND 255
        ),
    UNIQUE (assignment_id, delivery_id),
    UNIQUE (assignment_id, delivery_id, endpoint_snapshot_id),
    UNIQUE (assignment_id, delivery_id, ingest_id, payload_id, endpoint_snapshot_id),
    FOREIGN KEY (assignment_id, ingest_id)
        REFERENCES event_ingests (assignment_id, ingest_id),
    FOREIGN KEY (assignment_id, payload_id)
        REFERENCES payload_records (assignment_id, payload_id),
    FOREIGN KEY (assignment_id, endpoint_snapshot_id)
        REFERENCES endpoint_snapshots (assignment_id, endpoint_snapshot_id)
);

CREATE TABLE durable_jobs (
    job_id text PRIMARY KEY,
    assignment_id text NOT NULL,
    kind text NOT NULL,
    ingest_id text,
    payload_id text,
    delivery_id text,
    endpoint_snapshot_id text,
    state hookship_work_state NOT NULL DEFAULT 'ready',
    run_at timestamptz NOT NULL,
    lease_owner text,
    lease_token text,
    lease_expires_at timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL,
    last_error_code text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    completed_at timestamptz,
    CONSTRAINT durable_jobs_id_lengths
        CHECK (
            length(job_id) BETWEEN 1 AND 255
            AND length(assignment_id) BETWEEN 1 AND 255
            AND length(kind) BETWEEN 1 AND 64
            AND (lease_owner IS NULL OR length(lease_owner) BETWEEN 1 AND 255)
            AND (lease_token IS NULL OR length(lease_token) BETWEEN 1 AND 255)
            AND (last_error_code IS NULL OR length(last_error_code) <= 128)
        ),
    CONSTRAINT durable_jobs_attempt_bounds
        CHECK (attempts >= 0 AND max_attempts BETWEEN 1 AND 1000),
    CONSTRAINT durable_jobs_lease_shape
        CHECK (
            (
                state = 'leased'
                AND lease_owner IS NOT NULL
                AND lease_token IS NOT NULL
                AND lease_expires_at IS NOT NULL
            )
            OR (
                state <> 'leased'
                AND lease_owner IS NULL
                AND lease_token IS NULL
                AND lease_expires_at IS NULL
            )
        ),
    CONSTRAINT durable_jobs_terminal_shape
        CHECK (
            (state IN ('succeeded', 'dead') AND completed_at IS NOT NULL)
            OR (state NOT IN ('succeeded', 'dead') AND completed_at IS NULL)
        ),
    CONSTRAINT durable_jobs_state_allowed
        CHECK (state <> 'published'),
    CONSTRAINT durable_jobs_kind_shape
        CHECK (
            (
                kind = 'dispatch'
                AND ingest_id IS NOT NULL
                AND payload_id IS NOT NULL
                AND delivery_id IS NOT NULL
                AND endpoint_snapshot_id IS NOT NULL
            )
            OR (
                kind IN ('payload_finalize', 'payload_delete')
                AND payload_id IS NOT NULL
            )
        ),
    UNIQUE (assignment_id, job_id),
    UNIQUE (assignment_id, job_id, delivery_id),
    FOREIGN KEY (assignment_id, ingest_id)
        REFERENCES event_ingests (assignment_id, ingest_id),
    FOREIGN KEY (assignment_id, payload_id)
        REFERENCES payload_records (assignment_id, payload_id),
    FOREIGN KEY (assignment_id, endpoint_snapshot_id)
        REFERENCES endpoint_snapshots (assignment_id, endpoint_snapshot_id),
    FOREIGN KEY (assignment_id, delivery_id, ingest_id, payload_id, endpoint_snapshot_id)
        REFERENCES deliveries (assignment_id, delivery_id, ingest_id, payload_id, endpoint_snapshot_id)
);

ALTER TABLE ingest_idempotency_ledgers
    ADD CONSTRAINT ingest_idempotency_ledgers_ingest_fk
    FOREIGN KEY (assignment_id, ingest_id)
    REFERENCES event_ingests (assignment_id, ingest_id)
    DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT ingest_idempotency_ledgers_payload_fk
    FOREIGN KEY (assignment_id, payload_id)
    REFERENCES payload_records (assignment_id, payload_id)
    DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT ingest_idempotency_ledgers_job_fk
    FOREIGN KEY (assignment_id, dispatch_job_id)
    REFERENCES durable_jobs (assignment_id, job_id)
    DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE delivery_attempts (
    attempt_id text PRIMARY KEY,
    assignment_id text NOT NULL,
    delivery_id text NOT NULL,
    job_id text NOT NULL,
    endpoint_snapshot_id text NOT NULL,
    attempt_number integer NOT NULL,
    outcome text NOT NULL,
    response_status integer,
    error_code text,
    duration_ms integer NOT NULL,
    started_at timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT delivery_attempts_id_lengths
        CHECK (
            length(attempt_id) BETWEEN 1 AND 255
            AND length(assignment_id) BETWEEN 1 AND 255
            AND length(outcome) BETWEEN 1 AND 64
            AND (error_code IS NULL OR length(error_code) <= 128)
        ),
    CONSTRAINT delivery_attempts_bounds
        CHECK (
            attempt_number BETWEEN 1 AND 1000
            AND duration_ms BETWEEN 0 AND 3600000
            AND (response_status IS NULL OR response_status BETWEEN 100 AND 599)
            AND finished_at >= started_at
        ),
    UNIQUE (assignment_id, attempt_id),
    UNIQUE (assignment_id, delivery_id, attempt_number),
    UNIQUE (assignment_id, attempt_id, delivery_id, endpoint_snapshot_id),
    FOREIGN KEY (assignment_id, delivery_id, endpoint_snapshot_id)
        REFERENCES deliveries (assignment_id, delivery_id, endpoint_snapshot_id),
    FOREIGN KEY (assignment_id, job_id, delivery_id)
        REFERENCES durable_jobs (assignment_id, job_id, delivery_id)
);

CREATE TABLE metadata_outbox (
    outbox_id text PRIMARY KEY,
    assignment_id text NOT NULL,
    delivery_id text NOT NULL,
    attempt_id text NOT NULL,
    endpoint_snapshot_id text NOT NULL,
    schema_version text NOT NULL,
    event_kind text NOT NULL,
    outcome text NOT NULL,
    response_status integer,
    error_code text,
    occurred_at timestamptz NOT NULL,
    state hookship_work_state NOT NULL DEFAULT 'ready',
    run_at timestamptz NOT NULL,
    lease_owner text,
    lease_token text,
    lease_expires_at timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL,
    last_error_code text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    published_at timestamptz,
    completed_at timestamptz,
    CONSTRAINT metadata_outbox_id_lengths
        CHECK (
            length(outbox_id) BETWEEN 1 AND 255
            AND length(assignment_id) BETWEEN 1 AND 255
            AND length(schema_version) BETWEEN 1 AND 64
            AND length(event_kind) BETWEEN 1 AND 64
            AND length(outcome) BETWEEN 1 AND 64
            AND (error_code IS NULL OR length(error_code) <= 128)
            AND (last_error_code IS NULL OR length(last_error_code) <= 128)
        ),
    CONSTRAINT metadata_outbox_status
        CHECK (response_status IS NULL OR response_status BETWEEN 100 AND 599),
    CONSTRAINT metadata_outbox_attempt_bounds
        CHECK (attempts >= 0 AND max_attempts BETWEEN 1 AND 1000),
    CONSTRAINT metadata_outbox_lease_shape
        CHECK (
            (
                state = 'leased'
                AND lease_owner IS NOT NULL
                AND lease_token IS NOT NULL
                AND lease_expires_at IS NOT NULL
            )
            OR (
                state <> 'leased'
                AND lease_owner IS NULL
                AND lease_token IS NULL
                AND lease_expires_at IS NULL
            )
        ),
    CONSTRAINT metadata_outbox_terminal_shape
        CHECK (
            (state = 'published' AND published_at IS NOT NULL AND completed_at IS NOT NULL)
            OR (state = 'dead' AND published_at IS NULL AND completed_at IS NOT NULL)
            OR (state NOT IN ('published', 'dead') AND published_at IS NULL AND completed_at IS NULL)
        ),
    CONSTRAINT metadata_outbox_state_allowed
        CHECK (state <> 'succeeded'),
    UNIQUE (assignment_id, outbox_id),
    FOREIGN KEY (assignment_id, attempt_id, delivery_id, endpoint_snapshot_id)
        REFERENCES delivery_attempts (assignment_id, attempt_id, delivery_id, endpoint_snapshot_id)
);

CREATE UNIQUE INDEX durable_jobs_one_dispatch_per_delivery
    ON durable_jobs (assignment_id, delivery_id)
    WHERE kind = 'dispatch';

CREATE INDEX ingest_idempotency_ledgers_pending_recovery
    ON ingest_idempotency_ledgers (reserved_at, assignment_id)
    WHERE state = 'pending';

CREATE INDEX ingest_idempotency_ledgers_commit_retention
    ON ingest_idempotency_ledgers (idempotency_expires_at, assignment_id)
    WHERE state = 'committed';

CREATE INDEX ingest_idempotency_ledgers_tombstone_retention
    ON ingest_idempotency_ledgers (tombstone_expires_at, assignment_id)
    WHERE state = 'tombstone';

CREATE INDEX event_ingests_retention
    ON event_ingests (accepted_at, assignment_id);

CREATE INDEX payload_records_retention
    ON payload_records (expires_at, assignment_id)
    WHERE state IN ('staging', 'finalized', 'committed', 'orphaned', 'deleting');

CREATE INDEX payload_records_deletion_reclaim
    ON payload_records (assignment_id, deletion_lease_expires_at, expires_at)
    WHERE state = 'deleting';

CREATE INDEX payload_stage_write_attempts_active
    ON payload_stage_write_attempts (assignment_id, payload_id, lease_expires_at)
    WHERE state = 'leased';

CREATE INDEX payload_stage_write_attempts_cleanup
    ON payload_stage_write_attempts (assignment_id, payload_id, attempt_number)
    WHERE state IN ('orphaned', 'promoted');

CREATE INDEX durable_jobs_claim
    ON durable_jobs (assignment_id, kind, run_at, created_at)
    WHERE state IN ('ready', 'retry', 'leased');

CREATE INDEX durable_jobs_dead_retention
    ON durable_jobs (completed_at, assignment_id)
    WHERE state = 'dead';

CREATE INDEX delivery_attempts_retention
    ON delivery_attempts (finished_at, assignment_id);

CREATE INDEX metadata_outbox_claim
    ON metadata_outbox (assignment_id, run_at, created_at)
    WHERE state IN ('ready', 'retry', 'leased');

CREATE INDEX metadata_outbox_retention
    ON metadata_outbox (completed_at, assignment_id)
    WHERE state IN ('published', 'dead');

CREATE FUNCTION hookship_reject_immutable_row()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% rows are immutable', TG_TABLE_NAME
        USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER event_ingests_immutable
    BEFORE UPDATE OR DELETE ON event_ingests
    FOR EACH ROW EXECUTE FUNCTION hookship_reject_immutable_row();

CREATE TRIGGER endpoint_snapshots_immutable
    BEFORE UPDATE OR DELETE ON endpoint_snapshots
    FOR EACH ROW EXECUTE FUNCTION hookship_reject_immutable_row();

CREATE TRIGGER delivery_attempts_immutable
    BEFORE UPDATE OR DELETE ON delivery_attempts
    FOR EACH ROW EXECUTE FUNCTION hookship_reject_immutable_row();

CREATE FUNCTION hookship_guard_idempotency_ledger()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'idempotency ledger rows are retained'
            USING ERRCODE = '55000';
    END IF;

    IF OLD.ledger_id IS DISTINCT FROM NEW.ledger_id
        OR OLD.assignment_id IS DISTINCT FROM NEW.assignment_id
        OR OLD.idempotency_key IS DISTINCT FROM NEW.idempotency_key
        OR OLD.request_fingerprint IS DISTINCT FROM NEW.request_fingerprint
        OR OLD.body_sha256 IS DISTINCT FROM NEW.body_sha256
        OR OLD.event_id IS DISTINCT FROM NEW.event_id
        OR OLD.event_type IS DISTINCT FROM NEW.event_type
        OR OLD.route IS DISTINCT FROM NEW.route
        OR OLD.content_type IS DISTINCT FROM NEW.content_type
        OR OLD.reserved_at IS DISTINCT FROM NEW.reserved_at
    THEN
        RAISE EXCEPTION 'idempotency identity and fingerprint are immutable'
            USING ERRCODE = '55000';
    END IF;

    IF OLD.state = 'pending' AND NEW.state NOT IN ('pending', 'committed')
        OR OLD.state = 'committed' AND NEW.state NOT IN ('committed', 'tombstone')
        OR OLD.state = 'tombstone' AND NEW.state <> 'tombstone'
    THEN
        RAISE EXCEPTION 'invalid idempotency state transition: % to %', OLD.state, NEW.state
            USING ERRCODE = '55000';
    END IF;

    IF OLD.state <> 'pending' AND (
        OLD.ingest_id IS DISTINCT FROM NEW.ingest_id
        OR OLD.payload_id IS DISTINCT FROM NEW.payload_id
        OR OLD.dispatch_job_id IS DISTINCT FROM NEW.dispatch_job_id
        OR OLD.accepted_at IS DISTINCT FROM NEW.accepted_at
        OR OLD.idempotency_expires_at IS DISTINCT FROM NEW.idempotency_expires_at
        OR OLD.tombstone_expires_at IS DISTINCT FROM NEW.tombstone_expires_at
    ) THEN
        RAISE EXCEPTION 'committed idempotency receipt is immutable'
            USING ERRCODE = '55000';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER ingest_idempotency_ledgers_guard
    BEFORE UPDATE OR DELETE ON ingest_idempotency_ledgers
    FOR EACH ROW EXECUTE FUNCTION hookship_guard_idempotency_ledger();

CREATE FUNCTION hookship_guard_payload_record()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'payload metadata rows are retained'
            USING ERRCODE = '55000';
    END IF;

    IF OLD.payload_id IS DISTINCT FROM NEW.payload_id
        OR OLD.assignment_id IS DISTINCT FROM NEW.assignment_id
        OR OLD.ledger_id IS DISTINCT FROM NEW.ledger_id
        OR OLD.backend IS DISTINCT FROM NEW.backend
        OR OLD.key_reference IS DISTINCT FROM NEW.key_reference
        OR OLD.key_version IS DISTINCT FROM NEW.key_version
        OR OLD.nonce IS DISTINCT FROM NEW.nonce
        OR OLD.ciphertext_sha256 IS DISTINCT FROM NEW.ciphertext_sha256
        OR OLD.ciphertext_size IS DISTINCT FROM NEW.ciphertext_size
        OR OLD.expires_at IS DISTINCT FROM NEW.expires_at
        OR OLD.created_at IS DISTINCT FROM NEW.created_at
        OR OLD.staged_at IS DISTINCT FROM NEW.staged_at
    THEN
        RAISE EXCEPTION 'payload identity and cryptographic metadata are immutable'
            USING ERRCODE = '55000';
    END IF;

    IF (
        OLD.staging_object_key IS DISTINCT FROM NEW.staging_object_key
        OR OLD.final_object_key IS DISTINCT FROM NEW.final_object_key
    )
        AND NOT (
            OLD.state = 'staging'
            AND NEW.state = 'staging'
            AND EXISTS (
                SELECT 1
                FROM payload_stage_write_attempts AS attempt
                WHERE attempt.assignment_id = NEW.assignment_id
                  AND attempt.payload_id = NEW.payload_id
                  AND attempt.staging_object_key = NEW.staging_object_key
                  AND attempt.final_object_key = NEW.final_object_key
                  AND attempt.state = 'leased'
            )
        )
    THEN
        RAISE EXCEPTION 'payload staging object identity can change only on a fenced write reclaim'
            USING ERRCODE = '55000';
    END IF;

    IF OLD.ingest_id IS DISTINCT FROM NEW.ingest_id
        AND NOT (OLD.ingest_id IS NULL AND NEW.ingest_id IS NOT NULL AND NEW.state = 'committed')
    THEN
        RAISE EXCEPTION 'payload ingest lineage is immutable after commit'
            USING ERRCODE = '55000';
    END IF;

    IF OLD.ciphertext IS DISTINCT FROM NEW.ciphertext
        AND NOT (OLD.state = 'deleting' AND NEW.state = 'deleted' AND NEW.ciphertext IS NULL)
    THEN
        RAISE EXCEPTION 'payload ciphertext is immutable before deletion'
            USING ERRCODE = '55000';
    END IF;

    IF OLD.deletion_started_at IS NOT NULL
        AND OLD.deletion_started_at IS DISTINCT FROM NEW.deletion_started_at
    THEN
        RAISE EXCEPTION 'payload deletion start time is immutable'
            USING ERRCODE = '55000';
    END IF;

    IF OLD.state = 'staging' AND NEW.state NOT IN ('staging', 'finalized', 'orphaned')
        OR OLD.state = 'finalized' AND NEW.state NOT IN ('finalized', 'committed', 'orphaned')
        OR OLD.state = 'committed' AND NEW.state NOT IN ('committed', 'deleting')
        OR OLD.state = 'orphaned' AND NEW.state NOT IN ('orphaned', 'deleting')
        OR OLD.state = 'deleting' AND NEW.state NOT IN ('deleting', 'deleted')
        OR OLD.state = 'deleted' AND NEW.state <> 'deleted'
    THEN
        RAISE EXCEPTION 'invalid payload state transition: % to %', OLD.state, NEW.state
            USING ERRCODE = '55000';
    END IF;

    IF OLD.state = 'committed' AND NEW.state = 'deleting' AND NOT EXISTS (
        SELECT 1
        FROM ingest_idempotency_ledgers AS ledger
        WHERE ledger.assignment_id = OLD.assignment_id
          AND ledger.ledger_id = OLD.ledger_id
          AND ledger.state = 'tombstone'
    ) THEN
        RAISE EXCEPTION 'committed payload deletion requires a tombstoned idempotency ledger'
            USING ERRCODE = '55000';
    END IF;

    IF OLD.state IN ('committed', 'orphaned') AND NEW.state = 'deleting' AND EXISTS (
        SELECT 1
        FROM durable_jobs AS job
        WHERE job.assignment_id = OLD.assignment_id
          AND job.payload_id = OLD.payload_id
          AND job.kind <> 'payload_delete'
          AND job.state IN ('ready', 'leased', 'retry')
    ) THEN
        RAISE EXCEPTION 'payload deletion requires terminal jobs'
            USING ERRCODE = '55000';
    END IF;

    IF OLD.state IN ('staging', 'finalized') AND NEW.state = 'orphaned' AND EXISTS (
        SELECT 1
        FROM payload_stage_write_attempts AS attempt
        WHERE attempt.assignment_id = OLD.assignment_id
          AND attempt.payload_id = OLD.payload_id
          AND attempt.state = 'leased'
          AND attempt.lease_expires_at > clock_timestamp()
    ) THEN
        RAISE EXCEPTION 'payload cannot be orphaned while a stage write lease is active'
            USING ERRCODE = '55000';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER payload_records_guard
    BEFORE UPDATE OR DELETE ON payload_records
    FOR EACH ROW EXECUTE FUNCTION hookship_guard_payload_record();

CREATE FUNCTION hookship_guard_stage_write_attempt()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'stage write attempt rows are retained for cleanup discovery'
            USING ERRCODE = '55000';
    END IF;

    IF OLD.stage_write_attempt_id IS DISTINCT FROM NEW.stage_write_attempt_id
        OR OLD.assignment_id IS DISTINCT FROM NEW.assignment_id
        OR OLD.payload_id IS DISTINCT FROM NEW.payload_id
        OR OLD.ledger_id IS DISTINCT FROM NEW.ledger_id
        OR OLD.attempt_number IS DISTINCT FROM NEW.attempt_number
        OR OLD.staging_object_key IS DISTINCT FROM NEW.staging_object_key
        OR OLD.final_object_key IS DISTINCT FROM NEW.final_object_key
        OR OLD.ciphertext_sha256 IS DISTINCT FROM NEW.ciphertext_sha256
        OR OLD.ciphertext_size IS DISTINCT FROM NEW.ciphertext_size
        OR OLD.created_at IS DISTINCT FROM NEW.created_at
    THEN
        RAISE EXCEPTION 'stage write attempt identity is immutable'
            USING ERRCODE = '55000';
    END IF;

    IF OLD.state = 'ready' AND NEW.state NOT IN ('ready', 'leased', 'orphaned')
        OR OLD.state = 'leased' AND NEW.state NOT IN ('leased', 'promoted', 'orphaned')
        OR OLD.state = 'promoted' AND NEW.state <> 'promoted'
        OR OLD.state = 'orphaned' AND NEW.state <> 'orphaned'
    THEN
        RAISE EXCEPTION 'invalid stage write state transition: % to %', OLD.state, NEW.state
            USING ERRCODE = '55000';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER payload_stage_write_attempts_guard
    BEFORE UPDATE OR DELETE ON payload_stage_write_attempts
    FOR EACH ROW EXECUTE FUNCTION hookship_guard_stage_write_attempt();

CREATE FUNCTION hookship_assert_committed_barrier()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.state = 'committed' AND NOT EXISTS (
        SELECT 1
        FROM event_ingests AS ingest
        JOIN payload_records AS payload
          ON payload.assignment_id = ingest.assignment_id
         AND payload.ingest_id = ingest.ingest_id
        JOIN deliveries AS delivery
          ON delivery.assignment_id = ingest.assignment_id
         AND delivery.ingest_id = ingest.ingest_id
         AND delivery.payload_id = payload.payload_id
        JOIN durable_jobs AS job
          ON job.assignment_id = delivery.assignment_id
         AND job.delivery_id = delivery.delivery_id
         AND job.ingest_id = delivery.ingest_id
         AND job.payload_id = delivery.payload_id
         AND job.endpoint_snapshot_id = delivery.endpoint_snapshot_id
        WHERE ingest.assignment_id = NEW.assignment_id
          AND ingest.ingest_id = NEW.ingest_id
          AND ingest.ledger_id = NEW.ledger_id
          AND ingest.request_fingerprint = NEW.request_fingerprint
          AND ingest.body_sha256 = NEW.body_sha256
          AND ingest.event_id = NEW.event_id
          AND ingest.event_type = NEW.event_type
          AND ingest.route = NEW.route
          AND ingest.content_type = NEW.content_type
          AND ingest.accepted_at = NEW.accepted_at
          AND ingest.idempotency_expires_at = NEW.idempotency_expires_at
          AND payload.payload_id = NEW.payload_id
          AND payload.ledger_id = NEW.ledger_id
          AND payload.state = 'committed'
          AND delivery.state = 'ready'
          AND job.job_id = NEW.dispatch_job_id
          AND job.kind = 'dispatch'
          AND job.state = 'ready'
    ) THEN
        RAISE EXCEPTION 'committed idempotency ledger is missing the durable acceptance barrier'
            USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER ingest_idempotency_ledgers_commit_barrier
    AFTER INSERT OR UPDATE ON ingest_idempotency_ledgers
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hookship_assert_committed_barrier();

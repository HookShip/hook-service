-- 000003_object_restage.up.sql
--
-- Allow a fenced staging supersession for object-store payloads. When a
-- producer retries under the same idempotency key after a failure or crash, the
-- runtime re-seals the body with a fresh random AES-GCM nonce, producing a new
-- ciphertext identity. Before this migration the payload guard held the nonce,
-- checksum, and size immutable, so a retry could never match the staged identity
-- and the acceptance wedged (retryable 503 until the 24h payload expiry). GCM
-- nonces must never be reused, so the safe fix is to supersede the staged
-- identity under a fence rather than reuse the old nonce.
--
-- This replaces hookship_guard_payload_record to permit the ciphertext identity
-- (nonce, ciphertext_sha256, ciphertext_size) to change only while the payload
-- is 'staging' and only when a matching leased stage-write attempt already
-- records the new identity — i.e. a controlled, fenced supersession created by
-- SupersedeObjectStage. All other identity fields, and finalized/committed
-- payloads, remain immutable. The object-key change rule (already fenced on a
-- leased attempt) is unchanged, so superseded keys move together with the crypto
-- identity to the new attempt while prior attempts are retained for cleanup.

CREATE OR REPLACE FUNCTION hookship_guard_payload_record()
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
        OR OLD.expires_at IS DISTINCT FROM NEW.expires_at
        OR OLD.created_at IS DISTINCT FROM NEW.created_at
        OR OLD.staged_at IS DISTINCT FROM NEW.staged_at
    THEN
        RAISE EXCEPTION 'payload identity and cryptographic metadata are immutable'
            USING ERRCODE = '55000';
    END IF;

    IF (
        OLD.nonce IS DISTINCT FROM NEW.nonce
        OR OLD.ciphertext_sha256 IS DISTINCT FROM NEW.ciphertext_sha256
        OR OLD.ciphertext_size IS DISTINCT FROM NEW.ciphertext_size
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
                  AND attempt.ciphertext_sha256 = NEW.ciphertext_sha256
                  AND attempt.ciphertext_size = NEW.ciphertext_size
                  AND attempt.state = 'leased'
            )
        )
    THEN
        RAISE EXCEPTION 'payload ciphertext identity can change only on a fenced staging supersession'
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

package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// This file defines the leased-work invariants shared by the job and outbox
// queues in exactly one place: the fence predicate, the exhaustion/DLQ
// transition, and the RowsAffected==1 settlement rule. The job and outbox tables
// differ only in their name and id column, so those are captured as compile-time
// constants and interpolated into otherwise-static SQL. No runtime value is ever
// interpolated, so there is no dynamic-SQL injection surface.

// leasedTable names a leased-work table and its identity column. Every field is
// a compile-time constant.
type leasedTable struct {
	table   string // physical table name
	idCol   string // primary work-id column
	label   string // human label for error messages
	hasKind bool   // whether the table is partitioned by a `kind` column (jobs)
}

var (
	jobTable    = leasedTable{table: "durable_jobs", idCol: "job_id", label: "job", hasKind: true}
	outboxTable = leasedTable{table: "metadata_outbox", idCol: "outbox_id", label: "outbox row"}
)

// leaseKey is the fencing identity of a settlement: only the owner holding the
// exact token at the exact attempt on an unexpired lease may settle the work.
type leaseKey struct {
	assignmentID string
	id           string
	owner        string
	token        string
	attempt      int
}

func jobLeaseKey(lease JobLease) leaseKey {
	return leaseKey{lease.AssignmentID, lease.JobID, lease.Owner, lease.Token, lease.Attempt}
}

func outboxLeaseKey(lease OutboxLease) leaseKey {
	return leaseKey{lease.AssignmentID, lease.OutboxID, lease.Owner, lease.Token, lease.Attempt}
}

// Settlement SET clauses. Each is a compile-time constant matching the exact
// column writes of its operation; positional parameters $6+ line up with the
// extra args passed to settleLeased after the five fence parameters.
const (
	setComplete = `
			state = 'succeeded',
			lease_owner = NULL,
			lease_token = NULL,
			lease_expires_at = NULL,
			completed_at = clock.now,
			updated_at = clock.now,
			last_error_code = NULL`

	setRetry = `
			state = CASE WHEN attempts >= max_attempts THEN 'dead'::hookship_work_state ELSE 'retry'::hookship_work_state END,
			run_at = clock.now + $6::bigint * interval '1 microsecond',
			lease_owner = NULL,
			lease_token = NULL,
			lease_expires_at = NULL,
			completed_at = CASE WHEN attempts >= max_attempts THEN clock.now ELSE NULL END,
			updated_at = clock.now,
			last_error_code = $7`

	setFail = `
			state = 'dead',
			run_at = clock.now,
			lease_owner = NULL,
			lease_token = NULL,
			lease_expires_at = NULL,
			completed_at = clock.now,
			updated_at = clock.now,
			last_error_code = $6`

	setPublish = `
			state = 'published',
			lease_owner = NULL,
			lease_token = NULL,
			lease_expires_at = NULL,
			published_at = clock.now,
			completed_at = clock.now,
			updated_at = clock.now,
			last_error_code = NULL`
)

// settleLeased applies a fenced settlement UPDATE to a leased-work table. The
// fence predicate (owner+token+attempt match on an unexpired lease) and the
// settlement invariant (exactly one row affected, else ErrLeaseLost) live here
// and nowhere else. setClause is a compile-time constant; extraArgs bind its
// $6+ parameters in order.
func (b base) settleLeased(ctx context.Context, t leasedTable, op, setClause string, key leaseKey, extraArgs ...any) error {
	sql := fmt.Sprintf(`
		WITH clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		)
		UPDATE %s
		SET%s
		FROM clock
		WHERE assignment_id = $1
		  AND %s = $2
		  AND state = 'leased'
		  AND lease_owner = $3
		  AND lease_token = $4
		  AND attempts = $5
		  AND lease_expires_at > clock.now
	`, t.table, setClause, t.idCol)

	args := append([]any{key.assignmentID, key.id, key.owner, key.token, key.attempt}, extraArgs...)
	tag, err := b.pool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %w", op, t.label, err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

// markExhaustedDead moves every claimable-but-exhausted row (attempts at or over
// the cap and due to run) to the dead-letter state before a claim pass, so an
// exhausted row is never re-leased. This is the single definition of the
// exhaustion/DLQ boundary for both queues.
func (b base) markExhaustedDead(ctx context.Context, tx pgx.Tx, t leasedTable, assignmentID, kind string) error {
	kindFilter := ""
	args := []any{assignmentID}
	if t.hasKind {
		kindFilter = "\n\t\t  AND kind = $2"
		args = append(args, kind)
	}
	sql := fmt.Sprintf(`
		WITH clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		)
		UPDATE %s
		SET
			state = 'dead',
			lease_owner = NULL,
			lease_token = NULL,
			lease_expires_at = NULL,
			completed_at = clock.now,
			updated_at = clock.now,
			last_error_code = COALESCE(last_error_code, 'attempts_exhausted')
		FROM clock
		WHERE assignment_id = $1%s
		  AND attempts >= max_attempts
		  AND run_at <= clock.now
		  AND (
			  state IN ('ready', 'retry')
			  OR (state = 'leased' AND lease_expires_at <= clock.now)
		  )
	`, t.table, kindFilter)
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("mark exhausted %ss dead: %w", t.label, err)
	}
	return nil
}

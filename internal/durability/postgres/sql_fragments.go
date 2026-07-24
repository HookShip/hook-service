package postgres

// Shared SQL fragments and row scanners. Only compile-time constant identifiers
// are defined here and interpolated into otherwise-static SQL, so there is no
// dynamic-SQL injection surface. Fragments are extracted only where they remove
// a duplicated projection/scan pair and keep the scan order in lockstep with the
// column list; query-specific SQL (clocks, WHERE clauses) is left inline so each
// query remains a single readable literal.

// jobColumns is the canonical durable-job projection, aliased `job`. It is the
// exact column order scanJob expects, so a SELECT using it can never drift from
// the scan.
const jobColumns = `
			job.job_id,
			job.assignment_id,
			job.kind,
			COALESCE(job.ingest_id, ''),
			COALESCE(job.payload_id, ''),
			COALESCE(job.delivery_id, ''),
			COALESCE(job.endpoint_snapshot_id, ''),
			job.state::text,
			job.run_at,
			job.attempts,
			job.max_attempts,
			COALESCE(job.last_error_code, '')`

// rowScanner is satisfied by both pgx.Row and pgx.Rows, so a single scanner
// serves single-row and iterated queries.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanJob scans the jobColumns projection into a Job in one place, keeping every
// job-listing query's scan identical.
func scanJob(row rowScanner) (Job, error) {
	var job Job
	if err := row.Scan(
		&job.JobID,
		&job.AssignmentID,
		&job.Kind,
		&job.IngestID,
		&job.PayloadID,
		&job.DeliveryID,
		&job.EndpointSnapshotID,
		&job.State,
		&job.RunAt,
		&job.Attempts,
		&job.MaxAttempts,
		&job.LastErrorCode,
	); err != nil {
		return Job{}, err
	}
	return job, nil
}

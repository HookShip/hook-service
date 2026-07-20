package memory

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/hookship/hook-service/internal/runtime"
)

// ClaimJob claims one ready or reclaimable job for an assignment under a fencing
// lease, incrementing the attempt counter. Exhausted jobs are marked dead.
func (b *Backend) ClaimJob(_ context.Context, assignmentID, kind, owner, token string, lease time.Duration) (runtime.Job, bool, error) {
	if lease <= 0 || lease > time.Hour {
		return runtime.Job{}, false, errors.New("job lease duration must be between 1ns and 1h")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.Now()

	// Reap exhausted claimable jobs first.
	for _, entry := range b.jobs {
		if entry.job.AssignmentID != assignmentID || entry.job.Kind != kind {
			continue
		}
		if entry.job.Attempts >= entry.job.MaxAttempts && claimable(entry, now) {
			entry.job.State = "dead"
			entry.leaseOwner, entry.leaseToken = "", ""
			entry.leaseExpiry = time.Time{}
			entry.completedAt = now
			if entry.job.LastErrorCode == "" {
				entry.job.LastErrorCode = "attempts_exhausted"
			}
		}
	}

	candidate := b.oldestClaimableLocked(assignmentID, kind, now)
	if candidate == nil {
		return runtime.Job{}, false, nil
	}
	candidate.job.State = "leased"
	candidate.job.Attempts++
	candidate.leaseOwner = owner
	candidate.leaseToken = token
	candidate.leaseExpiry = now.Add(lease)
	expiry := candidate.leaseExpiry
	job := candidate.job
	job.LeaseExpiresAt = &expiry
	return job, true, nil
}

func claimable(entry *jobEntry, now time.Time) bool {
	if !entry.job.RunAt.After(now) {
		switch entry.job.State {
		case "ready", "retry":
			return true
		case "leased":
			return !entry.leaseExpiry.After(now)
		}
	}
	return false
}

func (b *Backend) oldestClaimableLocked(assignmentID, kind string, now time.Time) *jobEntry {
	var candidates []*jobEntry
	for _, entry := range b.jobs {
		if entry.job.AssignmentID != assignmentID || entry.job.Kind != kind {
			continue
		}
		if entry.job.Attempts >= entry.job.MaxAttempts {
			continue
		}
		if claimable(entry, now) {
			candidates = append(candidates, entry)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].job.RunAt.Equal(candidates[j].job.RunAt) {
			return candidates[i].job.RunAt.Before(candidates[j].job.RunAt)
		}
		if !candidates[i].createdAt.Equal(candidates[j].createdAt) {
			return candidates[i].createdAt.Before(candidates[j].createdAt)
		}
		return candidates[i].job.JobID < candidates[j].job.JobID
	})
	return candidates[0]
}

// LoadDelivery loads the committed delivery context for a claimed job, decrypting
// the payload into worker memory only.
func (b *Backend) LoadDelivery(_ context.Context, job runtime.Job) (runtime.DeliveryContext, error) {
	b.mu.Lock()
	endpoint, ok := b.endpoints[job.EndpointSnapshotID]
	payload, payloadOK := b.payloads[job.PayloadID]
	ingest, ingestOK := b.ingests[job.IngestID]
	b.mu.Unlock()
	if !ok || !payloadOK || !ingestOK {
		return runtime.DeliveryContext{}, runtime.ErrNotFound
	}
	plaintext, err := b.sealer.Open(payload.sealed)
	if err != nil {
		return runtime.DeliveryContext{}, err
	}
	return runtime.DeliveryContext{
		Job:          job,
		Endpoint:     endpoint,
		EventID:      ingest.headers.EventID,
		EventType:    ingest.headers.EventType,
		ContentType:  ingest.headers.ContentType,
		Body:         plaintext,
		BodySHA256:   ingest.bodySHA256,
		AssignmentID: job.AssignmentID,
	}, nil
}

// CompleteJob settles a successful job if the lease is still held.
func (b *Backend) CompleteJob(_ context.Context, lease runtime.JobLease) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.heldJobLocked(lease)
	if entry == nil {
		return runtime.ErrLeaseLost
	}
	entry.job.State = "succeeded"
	entry.leaseOwner, entry.leaseToken = "", ""
	entry.leaseExpiry = time.Time{}
	entry.completedAt = b.clock.Now()
	entry.job.LastErrorCode = ""
	b.touchIngestLocked(entry.job)
	return nil
}

// RetryJob reschedules a job after a delay, moving it to dead when attempts are
// exhausted.
func (b *Backend) RetryJob(_ context.Context, lease runtime.JobLease, delay time.Duration, errorCode string) error {
	if delay < 0 {
		return errors.New("retry delay must not be negative")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.heldJobLocked(lease)
	if entry == nil {
		return runtime.ErrLeaseLost
	}
	now := b.clock.Now()
	entry.leaseOwner, entry.leaseToken = "", ""
	entry.leaseExpiry = time.Time{}
	entry.job.LastErrorCode = errorCode
	if entry.job.Attempts >= entry.job.MaxAttempts {
		entry.job.State = "dead"
		entry.completedAt = now
	} else {
		entry.job.State = "retry"
		entry.job.RunAt = now.Add(delay)
	}
	b.touchIngestLocked(entry.job)
	return nil
}

// FailJob terminally dead-letters a leased job regardless of remaining attempts.
func (b *Backend) FailJob(_ context.Context, lease runtime.JobLease, errorCode string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.heldJobLocked(lease)
	if entry == nil {
		return runtime.ErrLeaseLost
	}
	entry.leaseOwner, entry.leaseToken = "", ""
	entry.leaseExpiry = time.Time{}
	entry.job.State = "dead"
	entry.job.LastErrorCode = errorCode
	entry.completedAt = b.clock.Now()
	b.touchIngestLocked(entry.job)
	return nil
}

func (b *Backend) heldJobLocked(lease runtime.JobLease) *jobEntry {
	entry, ok := b.jobs[lease.JobID]
	if !ok {
		return nil
	}
	now := b.clock.Now()
	if entry.job.AssignmentID != lease.AssignmentID ||
		entry.job.State != "leased" ||
		entry.leaseOwner != lease.Owner ||
		entry.leaseToken != lease.Token ||
		entry.job.Attempts != lease.Attempt ||
		!entry.leaseExpiry.After(now) {
		return nil
	}
	return entry
}

func (b *Backend) touchIngestLocked(job runtime.Job) {
	if record, ok := b.ingests[job.IngestID]; ok {
		record.updatedAt = b.clock.Now()
	}
}

// RecordAttempt writes an immutable attempt and its metadata outbox row.
func (b *Backend) RecordAttempt(_ context.Context, attempt runtime.AttemptRecord, observation runtime.OutboxObservation) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.attempts) >= b.limits.MaxAttempts {
		// Drop the oldest attempt to stay bounded; attempts are diagnostic only.
		b.attempts = b.attempts[1:]
	}
	b.attempts = append(b.attempts, attempt)
	if len(b.outbox) >= b.limits.MaxOutbox {
		return runtime.ErrCapacity
	}
	now := b.clock.Now()
	item := runtime.OutboxItem{
		OutboxObservation: observation,
		State:             "ready",
		Attempts:          0,
	}
	b.outbox[observation.OutboxID] = &outboxEntry{item: item, createdAt: now}
	return nil
}

// ClaimOutbox claims one ready metadata outbox row under a fencing lease.
func (b *Backend) ClaimOutbox(_ context.Context, assignmentID, owner, token string, lease time.Duration) (runtime.OutboxItem, bool, error) {
	if lease <= 0 || lease > time.Hour {
		return runtime.OutboxItem{}, false, errors.New("outbox lease duration must be between 1ns and 1h")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.Now()

	var candidates []*outboxEntry
	for _, entry := range b.outbox {
		if entry.item.AssignmentID != assignmentID {
			continue
		}
		if entry.item.Attempts >= entry.item.MaxAttempts {
			continue
		}
		if outboxClaimable(entry, now) {
			candidates = append(candidates, entry)
		}
	}
	if len(candidates) == 0 {
		return runtime.OutboxItem{}, false, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].item.RunAt.Equal(candidates[j].item.RunAt) {
			return candidates[i].item.RunAt.Before(candidates[j].item.RunAt)
		}
		return candidates[i].item.OutboxID < candidates[j].item.OutboxID
	})
	entry := candidates[0]
	entry.item.State = "leased"
	entry.item.Attempts++
	entry.leaseOwner = owner
	entry.leaseToken = token
	entry.leaseExpiry = now.Add(lease)
	expiry := entry.leaseExpiry
	item := entry.item
	item.LeaseExpiresAt = &expiry
	return item, true, nil
}

func outboxClaimable(entry *outboxEntry, now time.Time) bool {
	if entry.item.RunAt.After(now) {
		return false
	}
	switch entry.item.State {
	case "ready", "retry":
		return true
	case "leased":
		return !entry.leaseExpiry.After(now)
	}
	return false
}

// PublishOutbox marks a claimed outbox row published if the lease is still held.
func (b *Backend) PublishOutbox(_ context.Context, lease runtime.OutboxLease) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.heldOutboxLocked(lease)
	if entry == nil {
		return runtime.ErrLeaseLost
	}
	entry.item.State = "published"
	entry.leaseOwner, entry.leaseToken = "", ""
	entry.leaseExpiry = time.Time{}
	entry.item.LastErrorCode = ""
	return nil
}

// RetryOutbox reschedules a claimed outbox row.
func (b *Backend) RetryOutbox(_ context.Context, lease runtime.OutboxLease, delay time.Duration, errorCode string) error {
	if delay < 0 {
		return errors.New("retry delay must not be negative")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.heldOutboxLocked(lease)
	if entry == nil {
		return runtime.ErrLeaseLost
	}
	now := b.clock.Now()
	entry.leaseOwner, entry.leaseToken = "", ""
	entry.leaseExpiry = time.Time{}
	entry.item.LastErrorCode = errorCode
	if entry.item.Attempts >= entry.item.MaxAttempts {
		entry.item.State = "dead"
	} else {
		entry.item.State = "retry"
		entry.item.RunAt = now.Add(delay)
	}
	return nil
}

func (b *Backend) heldOutboxLocked(lease runtime.OutboxLease) *outboxEntry {
	entry, ok := b.outbox[lease.OutboxID]
	if !ok {
		return nil
	}
	now := b.clock.Now()
	if entry.item.AssignmentID != lease.AssignmentID ||
		entry.item.State != "leased" ||
		entry.leaseOwner != lease.Owner ||
		entry.leaseToken != lease.Token ||
		entry.item.Attempts != lease.Attempt ||
		!entry.leaseExpiry.After(now) {
		return nil
	}
	return entry
}

// ListDeadJobs returns terminal dead jobs for an assignment.
func (b *Backend) ListDeadJobs(_ context.Context, assignmentID string, limit int) ([]runtime.Job, error) {
	if limit < 1 || limit > 10_000 {
		return nil, errors.New("dead job limit must be between 1 and 10000")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var dead []runtime.Job
	for _, entry := range b.jobs {
		if entry.job.AssignmentID == assignmentID && entry.job.State == "dead" {
			dead = append(dead, entry.job)
		}
	}
	sort.Slice(dead, func(i, j int) bool { return dead[i].JobID < dead[j].JobID })
	if len(dead) > limit {
		dead = dead[:limit]
	}
	return dead, nil
}

// RedriveJob moves a dead job back to ready as an explicit operator action.
func (b *Backend) RedriveJob(_ context.Context, assignmentID, jobID string, runAt time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.jobs[jobID]
	if !ok || entry.job.AssignmentID != assignmentID {
		return runtime.ErrNotFound
	}
	if entry.job.State != "dead" {
		return errors.New("only dead jobs can be redriven")
	}
	if runAt.IsZero() {
		runAt = b.clock.Now()
	}
	entry.job.State = "ready"
	entry.job.Attempts = 0
	entry.job.RunAt = runAt
	entry.job.LastErrorCode = ""
	entry.completedAt = time.Time{}
	return nil
}

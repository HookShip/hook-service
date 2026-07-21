package ingest

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// problemDefinition binds a stable code to its HTTP status, type, title,
// detail, and retryable flag, matching spec/v1/schemas/problem-details.schema.json.
type problemDefinition struct {
	status    int
	code      string
	typeURI   string
	title     string
	detail    string
	retryable bool
}

const (
	codeMalformedRequest     = "malformed_request"
	codeAuthenticationFailed = "authentication_failed"
	codeAssignmentForbidden  = "assignment_forbidden"
	codeIngestNotFound       = "ingest_not_found"
	codeReplayDetected       = "replay_detected"
	codeIdempotencyConflict  = "idempotency_conflict"
	codeIdempotencyMetadata  = "idempotency_metadata_conflict"
	codeIdempotencyExpired   = "idempotency_expired"
	codePayloadTooLarge      = "payload_too_large"
	codeUnsupportedEncoding  = "unsupported_content_encoding"
	codeBodyHashMismatch     = "body_hash_mismatch"
	codeInvalidEvent         = "invalid_event"
	codeIngestThrottled      = "ingest_throttled"
	codeIngestUnavailable    = "ingest_unavailable"
)

var problems = map[string]problemDefinition{
	codeMalformedRequest:     {400, codeMalformedRequest, "urn:hookship:problem:malformed-request", "Malformed request", "The request could not be processed.", false},
	codeAuthenticationFailed: {401, codeAuthenticationFailed, "urn:hookship:problem:authentication-failed", "Authentication failed", "The request could not be authenticated.", false},
	codeAssignmentForbidden:  {403, codeAssignmentForbidden, "urn:hookship:problem:assignment-forbidden", "Assignment forbidden", "The assignment does not authorize this request.", false},
	codeIngestNotFound:       {404, codeIngestNotFound, "urn:hookship:problem:ingest-not-found", "Ingest not found", "The ingest was not found.", false},
	codeReplayDetected:       {409, codeReplayDetected, "urn:hookship:problem:replay-detected", "Replay detected", "The nonce was already used; retry with fresh replay fields.", true},
	codeIdempotencyConflict:  {409, codeIdempotencyConflict, "urn:hookship:problem:idempotency-conflict", "Idempotency conflict", "The idempotency key was reused with a different body.", false},
	codeIdempotencyMetadata:  {409, codeIdempotencyMetadata, "urn:hookship:problem:idempotency-metadata-conflict", "Idempotency metadata conflict", "The idempotency key was reused with different stable headers.", false},
	codeIdempotencyExpired:   {409, codeIdempotencyExpired, "urn:hookship:problem:idempotency-expired", "Idempotency expired", "The idempotency key is tombstoned; start a new operation.", false},
	codePayloadTooLarge:      {413, codePayloadTooLarge, "urn:hookship:problem:payload-too-large", "Payload too large", "The request body exceeded the assignment limit.", false},
	codeUnsupportedEncoding:  {415, codeUnsupportedEncoding, "urn:hookship:problem:unsupported-content-encoding", "Unsupported content encoding", "Remove content encoding; only identity is accepted.", false},
	codeBodyHashMismatch:     {422, codeBodyHashMismatch, "urn:hookship:problem:body-hash-mismatch", "Body hash mismatch", "The declared body hash did not match the received bytes.", false},
	codeInvalidEvent:         {422, codeInvalidEvent, "urn:hookship:problem:invalid-event", "Invalid event", "The event identifiers, media type, or route are invalid.", false},
	codeIngestThrottled:      {429, codeIngestThrottled, "urn:hookship:problem:ingest-throttled", "Ingest throttled", "The request was throttled; retry after the indicated delay.", true},
	codeIngestUnavailable:    {503, codeIngestUnavailable, "urn:hookship:problem:ingest-unavailable", "Ingest unavailable", "The durable acceptance barrier was not crossed; retry.", true},
}

// problemBody is the bounded RFC 9457 object. Its field order is fixed by JSON
// marshaling of this struct.
type problemBody struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	Instance  string `json:"instance"`
	Code      string `json:"code"`
	RequestID string `json:"requestId"`
	Retryable bool   `json:"retryable"`
}

// writeProblem emits a bounded application/problem+json response. The detail is
// never templated with request data, secrets, or payload bytes.
func writeProblem(w http.ResponseWriter, r *http.Request, code, requestID string, extraHeaders map[string]string) {
	definition, ok := problems[code]
	if !ok {
		definition = problems[codeMalformedRequest]
	}
	instance := r.URL.Path
	if instance == "" {
		instance = "/v1/event-ingests"
	}
	body := problemBody{
		Type:      definition.typeURI,
		Title:     definition.title,
		Status:    definition.status,
		Detail:    definition.detail,
		Instance:  instance,
		Code:      definition.code,
		RequestID: requestID,
		Retryable: definition.retryable,
	}

	header := w.Header()
	header.Set("Content-Type", "application/problem+json")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Hookship-Request-Id", requestID)
	for name, value := range extraHeaders {
		header.Set(name, value)
	}
	w.WriteHeader(definition.status)
	_ = json.NewEncoder(w).Encode(body)
}

func retryAfterHeader(seconds int) map[string]string {
	return map[string]string{"Retry-After": strconv.Itoa(seconds)}
}

package ingest_test

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/assignmentconfig"
	"github.com/hookship/hook-service/internal/backend/memory"
	"github.com/hookship/hook-service/internal/ingest"
	"github.com/hookship/hook-service/internal/runtime"
)

const evaluationSecretHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func newTestServer(t *testing.T) (*http.ServeMux, runtime.Backend) {
	t.Helper()
	base := assignmentconfig.DefaultEvaluationAssignment("http://127.0.0.1:9099/sink")
	backend, err := memory.New([]runtime.Assignment{base}, runtime.SystemClock{}, nil, memory.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := ingest.New(ingest.Options{Backend: backend, EvaluationMode: true})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	return mux, backend
}

type signedPost struct {
	body           []byte
	timestamp      string
	nonce          string
	idempotencyKey string
	eventID        string
	eventType      string
	route          string
	contentType    string
}

func defaultPost(body []byte) signedPost {
	return signedPost{
		body:           body,
		timestamp:      time.Now().UTC().Format(ingest.TimestampLayout),
		nonce:          "bm9uY2Ut" + randomToken(),
		idempotencyKey: "op-" + randomToken(),
		eventID:        "evt_" + randomToken(),
		eventType:      "order.created",
		route:          "orders.primary",
		contentType:    "application/json",
	}
}

func (p signedPost) request(t *testing.T) *http.Request {
	t.Helper()
	secret, _ := hex.DecodeString(evaluationSecretHex)
	bodyHash := runtime.SHA256(p.body).String()
	signed := ingest.SignedRequest{
		Method: http.MethodPost, Path: "/v1/event-ingests", CredentialID: assignmentconfig.EvaluationCredentialID,
		Timestamp: p.timestamp, Nonce: p.nonce, IdempotencyKey: p.idempotencyKey,
		EventID: p.eventID, EventType: p.eventType, Route: p.route, ContentType: p.contentType,
		BodySHA256Hex: bodyHash,
	}
	signature := ingest.ComputeSignature(secret, signed)
	request := httptest.NewRequest(http.MethodPost, "/v1/event-ingests", strings.NewReader(string(p.body)))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Authorization", "Hookship-HMAC Credential="+assignmentconfig.EvaluationCredentialID+",Signature="+signature)
	request.Header.Set("X-Hookship-Timestamp", p.timestamp)
	request.Header.Set("X-Hookship-Nonce", p.nonce)
	request.Header.Set("Idempotency-Key", p.idempotencyKey)
	request.Header.Set("X-Hookship-Event-ID", p.eventID)
	request.Header.Set("X-Hookship-Event-Type", p.eventType)
	request.Header.Set("X-Hookship-Route", p.route)
	request.Header.Set("X-Hookship-Body-SHA256", bodyHash)
	request.Header.Set("Content-Type", p.contentType)
	return request
}

func TestAcceptedAndStatus(t *testing.T) {
	mux, _ := newTestServer(t)
	post := defaultPost([]byte(`{"order_id":"123"}`))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, post.request(t))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Hookship-Duplicate") != "false" {
		t.Fatal("expected Hookship-Duplicate: false")
	}
	if recorder.Header().Get("Hookship-Durability") != "ephemeral" {
		t.Fatal("expected ephemeral durability marker header")
	}
	var receipt map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["state"] != "ephemeral" || receipt["durability"] != "ephemeral" {
		t.Fatalf("ephemeral receipt must not claim durability: %v", receipt)
	}
	ingestID, _ := receipt["ingestId"].(string)
	if !strings.HasPrefix(ingestID, "ing_") {
		t.Fatalf("bad ingest id %q", ingestID)
	}

	// Payload-free status GET.
	statusRequest := signedStatusRequest(t, ingestID)
	statusRecorder := httptest.NewRecorder()
	mux.ServeHTTP(statusRecorder, statusRequest)
	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("status GET = %d, body = %s", statusRecorder.Code, statusRecorder.Body.String())
	}
	if strings.Contains(statusRecorder.Body.String(), "order_id") {
		t.Fatal("status response leaked payload content")
	}
}

func TestDuplicateReturns200(t *testing.T) {
	mux, _ := newTestServer(t)
	post := defaultPost([]byte(`{"order_id":"456"}`))
	first := httptest.NewRecorder()
	mux.ServeHTTP(first, post.request(t))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first = %d", first.Code)
	}
	// A correct retry keeps the idempotency key/body but uses a fresh nonce/timestamp.
	retry := post
	retry.nonce = "bm9uY2UtcmV0cnk-" + randomToken()
	retry.timestamp = time.Now().UTC().Add(time.Second).Format(ingest.TimestampLayout)
	second := httptest.NewRecorder()
	mux.ServeHTTP(second, retry.request(t))
	if second.Code != http.StatusOK {
		t.Fatalf("duplicate retry = %d, body = %s", second.Code, second.Body.String())
	}
	if second.Header().Get("Hookship-Duplicate") != "true" {
		t.Fatal("expected Hookship-Duplicate: true on duplicate")
	}
}

func TestReplayDetected(t *testing.T) {
	mux, _ := newTestServer(t)
	post := defaultPost([]byte(`{"order_id":"789"}`))
	request := post.request(t)
	first := httptest.NewRecorder()
	mux.ServeHTTP(first, request)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first = %d", first.Code)
	}
	// Replaying the exact same signed request (same nonce) is a replay.
	replay := httptest.NewRecorder()
	mux.ServeHTTP(replay, post.request(t)) // same nonce/timestamp because fields fixed
	if replay.Code != http.StatusConflict {
		t.Fatalf("replay = %d, want 409", replay.Code)
	}
	assertProblemCode(t, replay.Body.Bytes(), "replay_detected")
}

func TestConflictOnDifferentBody(t *testing.T) {
	mux, _ := newTestServer(t)
	post := defaultPost([]byte(`{"order_id":"111"}`))
	first := httptest.NewRecorder()
	mux.ServeHTTP(first, post.request(t))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first = %d", first.Code)
	}
	conflict := post
	conflict.body = []byte(`{"order_id":"222"}`)
	conflict.nonce = "bm9uY2UtY29uZmxpY3Q-" + randomToken()
	conflict.timestamp = time.Now().UTC().Add(time.Second).Format(ingest.TimestampLayout)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, conflict.request(t))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("conflict = %d", recorder.Code)
	}
	assertProblemCode(t, recorder.Body.Bytes(), "idempotency_conflict")
}

func TestBadSignatureIsUnauthorized(t *testing.T) {
	mux, _ := newTestServer(t)
	post := defaultPost([]byte(`{"x":1}`))
	request := post.request(t)
	request.Header.Set("Authorization", "Hookship-HMAC Credential="+assignmentconfig.EvaluationCredentialID+",Signature=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature = %d", recorder.Code)
	}
	assertProblemCode(t, recorder.Body.Bytes(), "authentication_failed")
}

func TestBodyHashMismatch(t *testing.T) {
	mux, _ := newTestServer(t)
	post := defaultPost([]byte(`{"x":1}`))
	request := post.request(t)
	request.Header.Set("X-Hookship-Body-SHA256", runtime.SHA256([]byte("different")).String())
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("hash mismatch = %d", recorder.Code)
	}
}

func TestPayloadTooLarge(t *testing.T) {
	mux, _ := newTestServer(t)
	big := make([]byte, 128*1024)
	for i := range big {
		big[i] = 'a'
	}
	post := defaultPost(big)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, post.request(t))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize = %d", recorder.Code)
	}
}

func TestUnknownStatusIsNotFound(t *testing.T) {
	mux, _ := newTestServer(t)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, signedStatusRequest(t, "ing_00000000000000000000000000"))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d", recorder.Code)
	}
	assertProblemCode(t, recorder.Body.Bytes(), "ingest_not_found")
}

func signedStatusRequest(t *testing.T, ingestID string) *http.Request {
	t.Helper()
	secret, _ := hex.DecodeString(evaluationSecretHex)
	timestamp := time.Now().UTC().Format(ingest.TimestampLayout)
	nonce := "bm9uY2Utc3RhdHVz" + randomToken()
	bodyHash := runtime.SHA256(nil).String()
	signed := ingest.SignedRequest{
		Method: http.MethodGet, Path: "/v1/event-ingests/" + ingestID,
		CredentialID: assignmentconfig.EvaluationCredentialID, Timestamp: timestamp, Nonce: nonce,
		BodySHA256Hex: bodyHash,
	}
	signature := ingest.ComputeSignature(secret, signed)
	request := httptest.NewRequest(http.MethodGet, "/v1/event-ingests/"+ingestID, nil)
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Authorization", "Hookship-HMAC Credential="+assignmentconfig.EvaluationCredentialID+",Signature="+signature)
	request.Header.Set("X-Hookship-Timestamp", timestamp)
	request.Header.Set("X-Hookship-Nonce", nonce)
	request.Header.Set("X-Hookship-Body-SHA256", bodyHash)
	return request
}

func assertProblemCode(t *testing.T, body []byte, code string) {
	t.Helper()
	var problem map[string]any
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("problem body: %v (%s)", err, body)
	}
	if problem["code"] != code {
		t.Fatalf("problem code = %v, want %q", problem["code"], code)
	}
	if _, ok := problem["requestId"].(string); !ok {
		t.Fatal("problem missing requestId")
	}
}

var tokenCounter int

func randomToken() string {
	tokenCounter++
	return "tok" + hex.EncodeToString([]byte{byte(tokenCounter), byte(tokenCounter >> 8), byte(time.Now().UnixNano())})
}

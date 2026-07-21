package objectstore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hookship/hook-service/internal/awsv4"
	"github.com/hookship/hook-service/internal/runtime"
)

// fakeS3 is a minimal in-memory S3-compatible server sufficient to exercise the
// client's HEAD/PUT/GET/DELETE and immutability behavior. It requires a SigV4
// Authorization header so signing is verified end to end.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	meta    map[string]string
	// rejectIfNoneMatch enables the immutable-store behavior (412 on existing).
	rejectIfNoneMatch bool
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string][]byte{}, meta: map[string]string{}}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" || !strings.HasPrefix(r.Header.Get("Authorization"), awsv4.Algorithm) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.Header.Get("X-Amz-Content-Sha256") == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// Bucket-scoped list (Ping).
	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<ListBucketResult></ListBucketResult>`))
		return
	}
	key := r.URL.Path
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodHead:
		data, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("X-Amz-Meta-Sha256", f.meta[key])
		w.Header().Set("ETag", `"etag-`+f.meta[key][:8]+`"`)
		_ = data
		w.WriteHeader(http.StatusOK)
	case http.MethodPut:
		if _, ok := f.objects[key]; ok && f.rejectIfNoneMatch && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.objects[key] = body
		f.meta[key] = strings.ToLower(r.Header.Get("X-Amz-Meta-Sha256"))
		w.Header().Set("ETag", `"etag-`+f.meta[key][:8]+`"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		data, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("X-Amz-Meta-Sha256", f.meta[key])
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case http.MethodDelete:
		delete(f.objects, key)
		delete(f.meta, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := New(Config{
		Endpoint:    server.URL,
		Region:      "us-east-1",
		Bucket:      "hookship",
		PathStyle:   true,
		Credentials: awsv4.Credentials{AccessKeyID: "test", SecretAccessKey: "secret"},
		transportRT: server.Client().Transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestClientPutGetDeleteRoundTrip(t *testing.T) {
	server := httptest.NewServer(newFakeS3())
	defer server.Close()
	client := newTestClient(t, server)
	ctx := context.Background()

	data := []byte("sealed-ciphertext-bytes")
	checksum := runtime.SHA256(data)
	key := "hookship/v1/aa/bb/attempts/0000000001/final/ciphertext"

	result, err := client.PutImmutable(ctx, key, data, checksum)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if result.AlreadyExisted {
		t.Fatal("first put should not report AlreadyExisted")
	}
	if result.Version == "" {
		t.Fatal("expected a provider version")
	}

	got, err := client.Get(ctx, key, checksum)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("round-trip mismatch: %q", got)
	}

	// Idempotent re-put of identical bytes.
	again, err := client.PutImmutable(ctx, key, data, checksum)
	if err != nil {
		t.Fatalf("idempotent put: %v", err)
	}
	if !again.AlreadyExisted {
		t.Fatal("re-put of identical bytes should report AlreadyExisted")
	}

	if err := client.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := client.Get(ctx, key, checksum); err != runtime.ErrPayloadObjectNotFound {
		t.Fatalf("expected not found after delete, got %v", err)
	}
	// Delete is idempotent.
	if err := client.Delete(ctx, key); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestClientRejectsImmutabilityConflict(t *testing.T) {
	server := httptest.NewServer(newFakeS3())
	defer server.Close()
	client := newTestClient(t, server)
	ctx := context.Background()
	key := "hookship/v1/aa/bb/attempts/0000000001/final/ciphertext"

	first := []byte("original")
	if _, err := client.PutImmutable(ctx, key, first, runtime.SHA256(first)); err != nil {
		t.Fatalf("put: %v", err)
	}
	second := []byte("tampered")
	if _, err := client.PutImmutable(ctx, key, second, runtime.SHA256(second)); err != runtime.ErrPayloadObjectConflict {
		t.Fatalf("expected immutability conflict, got %v", err)
	}
}

func TestClientGetVerifiesChecksum(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	client := newTestClient(t, server)
	ctx := context.Background()
	key := "hookship/v1/aa/bb/attempts/0000000001/final/ciphertext"

	data := []byte("sealed")
	if _, err := client.PutImmutable(ctx, key, data, runtime.SHA256(data)); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Corrupt the stored object out from under the client.
	fake.mu.Lock()
	fake.objects["/hookship"+"/"+key] = []byte("corrupted-different-length")
	fake.mu.Unlock()
	if _, err := client.Get(ctx, key, runtime.SHA256(data)); err == nil {
		t.Fatal("expected checksum verification failure")
	}
}

func TestClientPingReachesBucket(t *testing.T) {
	server := httptest.NewServer(newFakeS3())
	defer server.Close()
	client := newTestClient(t, server)
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestNewRejectsIncompleteConfig(t *testing.T) {
	cases := []Config{
		{Region: "us-east-1", Bucket: "b", Credentials: awsv4.Credentials{AccessKeyID: "a", SecretAccessKey: "s"}},
		{Endpoint: "http://x", Bucket: "b", Credentials: awsv4.Credentials{AccessKeyID: "a", SecretAccessKey: "s"}},
		{Endpoint: "http://x", Region: "us-east-1", Credentials: awsv4.Credentials{AccessKeyID: "a", SecretAccessKey: "s"}},
		{Endpoint: "http://x", Region: "us-east-1", Bucket: "b"},
		{Endpoint: "http://x", Region: "us-east-1", Bucket: "b", UseTLS: true, Credentials: awsv4.Credentials{AccessKeyID: "a", SecretAccessKey: "s"}},
	}
	for i, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
}

func TestMemoryStoreContract(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	data := []byte("ciphertext")
	sum := runtime.SHA256(data)

	if _, err := store.PutImmutable(ctx, "k1", data, sum); err != nil {
		t.Fatal(err)
	}
	if store.ObjectCount() != 1 {
		t.Fatalf("object count = %d", store.ObjectCount())
	}
	got, err := store.Get(ctx, "k1", sum)
	if err != nil || string(got) != string(data) {
		t.Fatalf("get: %v %q", err, got)
	}
	other := []byte("different")
	if _, err := store.PutImmutable(ctx, "k1", other, runtime.SHA256(other)); err != runtime.ErrPayloadObjectConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
	if err := store.Delete(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "k1", sum); err != runtime.ErrPayloadObjectNotFound {
		t.Fatalf("expected not found, got %v", err)
	}
}

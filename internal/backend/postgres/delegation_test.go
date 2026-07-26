package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/runtime"
)

type fakeSealer struct {
	sealErr error
	openErr error
}

func (f fakeSealer) Seal(plaintext []byte) (runtime.SealedPayload, error) {
	if f.sealErr != nil {
		return runtime.SealedPayload{}, f.sealErr
	}
	return runtime.SealedPayload{Ciphertext: append([]byte("sealed:"), plaintext...)}, nil
}

func (f fakeSealer) Open(sealed runtime.SealedPayload) ([]byte, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return sealed.Ciphertext, nil
}

type fakeSource struct {
	assignment  runtime.Assignment
	assignments []runtime.Assignment
	resolveErr  error
}

func (f fakeSource) ResolveCredential(_ context.Context, _ string) (runtime.Assignment, error) {
	return f.assignment, f.resolveErr
}

func (f fakeSource) Assignments(_ context.Context) ([]runtime.Assignment, error) {
	return f.assignments, nil
}

func TestBackendPureAndDelegations(t *testing.T) {
	b := &Backend{
		sealer: fakeSealer{},
		source: fakeSource{
			assignment:  runtime.Assignment{ID: "asg_1"},
			assignments: []runtime.Assignment{{ID: "asg_1"}, {ID: "asg_2"}},
		},
	}
	if b.Name() != "postgres" {
		t.Fatalf("Name = %s", b.Name())
	}
	if !b.Durable() {
		t.Fatal("postgres backend must be durable")
	}

	sealed, err := b.Seal([]byte("hello"))
	if err != nil || string(sealed.Ciphertext) != "sealed:hello" {
		t.Fatalf("Seal = %q, %v", sealed.Ciphertext, err)
	}
	opened, err := b.Open(sealed)
	if err != nil || string(opened) != "sealed:hello" {
		t.Fatalf("Open = %q, %v", opened, err)
	}

	got, err := b.ResolveCredential(context.Background(), "cred")
	if err != nil || got.ID != "asg_1" {
		t.Fatalf("ResolveCredential = %+v, %v", got, err)
	}
	all, err := b.Assignments(context.Background())
	if err != nil || len(all) != 2 {
		t.Fatalf("Assignments = %d, %v", len(all), err)
	}
}

func TestBackendSealerErrorsPropagate(t *testing.T) {
	b := &Backend{sealer: fakeSealer{sealErr: errors.New("no key")}}
	if _, err := b.Seal([]byte("x")); err == nil {
		t.Fatal("seal error must propagate")
	}
	b.sealer = fakeSealer{openErr: errors.New("bad tag")}
	if _, err := b.Open(runtime.SealedPayload{}); err == nil {
		t.Fatal("open error must propagate")
	}
}

func TestBackendOptions(t *testing.T) {
	b := &Backend{}
	WithStageWriteLease(2 * time.Minute)(b)
	if b.stageLease() != 2*time.Minute {
		t.Fatalf("stage lease = %v", b.stageLease())
	}
	// A non-positive lease is ignored, falling back to the default.
	WithStageWriteLease(0)(b)
	if b.stageLease() != 2*time.Minute {
		t.Fatal("zero lease must be ignored")
	}

	fresh := &Backend{}
	if fresh.stageLease() != objectStageWriteLease {
		t.Fatalf("default stage lease = %v, want %v", fresh.stageLease(), objectStageWriteLease)
	}

	WithOwnedPool()(fresh)
	if !fresh.ownsPool {
		t.Fatal("WithOwnedPool must set ownsPool")
	}

	ids := runtime.NewULIDSource(runtime.SystemClock{})
	WithIDSource(ids)(fresh)
	if fresh.ids != ids {
		t.Fatal("WithIDSource must set the id source")
	}
	// A nil id source is ignored.
	WithIDSource(nil)(fresh)
	if fresh.ids != ids {
		t.Fatal("nil id source must be ignored")
	}
}

func TestBackendPayloadBackendNameObjectStore(t *testing.T) {
	// PayloadBackendName reports object-store when a store overlay is set.
	b := &Backend{store: stubPayloadStore{}}
	if b.PayloadBackendName() != "object-store" {
		t.Fatalf("payload backend = %s, want object-store", b.PayloadBackendName())
	}
}

type stubPayloadStore struct{}

func (stubPayloadStore) PutImmutable(context.Context, string, []byte, runtime.Hash) (runtime.ObjectPutResult, error) {
	return runtime.ObjectPutResult{}, nil
}
func (stubPayloadStore) Get(context.Context, string, runtime.Hash) ([]byte, error) { return nil, nil }
func (stubPayloadStore) Delete(context.Context, string) error                      { return nil }
func (stubPayloadStore) Ping(context.Context) error                                { return nil }
func (stubPayloadStore) Name() string                                              { return "stub" }

func TestMapObjectStoreError(t *testing.T) {
	if err := mapObjectStoreError("op", nil); err != nil {
		t.Fatalf("nil must map to nil, got %v", err)
	}
	// A hard immutability conflict is surfaced (wrapped), not collapsed to
	// retryable unavailability.
	conflict := mapObjectStoreError("put", runtime.ErrPayloadObjectConflict)
	if !errors.Is(conflict, runtime.ErrPayloadObjectConflict) {
		t.Fatalf("immutability conflict = %v, want wrapped ErrPayloadObjectConflict", conflict)
	}
	// Any other transient failure becomes retryable unavailability.
	if got := mapObjectStoreError("get", errors.New("timeout")); !errors.Is(got, runtime.ErrUnavailable) {
		t.Fatalf("transient error = %v, want ErrUnavailable", got)
	}
}

func TestProviderVersionFallbackToken(t *testing.T) {
	checksum := runtime.SHA256([]byte("x"))
	if got := providerVersion("etag", checksum); got != "etag" {
		t.Fatalf("versioned store = %q", got)
	}
	if got := providerVersion("", checksum); got != "sha256-"+checksum.String() {
		t.Fatalf("unversioned store = %q", got)
	}
}

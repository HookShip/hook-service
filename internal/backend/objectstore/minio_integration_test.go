//go:build integration

package objectstore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/awsv4"
	"github.com/hookship/hook-service/internal/runtime"
)

// TestMinIOIntegration exercises the S3 client against a real MinIO (or other
// S3-compatible) endpoint. It is skipped unless MINIO_TEST_ENDPOINT and the
// related environment variables are set, so `make check` stays dependency-free.
//
// Required environment:
//
//	MINIO_TEST_ENDPOINT   e.g. 127.0.0.1:59000
//	MINIO_TEST_BUCKET     e.g. hookship-payloads
//	MINIO_TEST_ACCESS_KEY e.g. hookshiptest
//	MINIO_TEST_SECRET_KEY e.g. hookshiptest123
//	MINIO_TEST_REGION     optional, defaults to us-east-1
func TestMinIOIntegration(t *testing.T) {
	endpoint := os.Getenv("MINIO_TEST_ENDPOINT")
	bucket := os.Getenv("MINIO_TEST_BUCKET")
	access := os.Getenv("MINIO_TEST_ACCESS_KEY")
	secret := os.Getenv("MINIO_TEST_SECRET_KEY")
	if endpoint == "" || bucket == "" || access == "" || secret == "" {
		t.Skip("MinIO integration environment is not configured")
	}
	region := os.Getenv("MINIO_TEST_REGION")
	if region == "" {
		region = "us-east-1"
	}

	client, err := New(Config{
		Endpoint:    endpoint,
		Region:      region,
		Bucket:      bucket,
		PathStyle:   true,
		UseTLS:      os.Getenv("MINIO_TEST_USE_TLS") == "true",
		Credentials: awsv4.Credentials{AccessKeyID: access, SecretAccessKey: secret},
		Timeout:     15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := client.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	data := []byte("hookship-minio-integration-ciphertext")
	checksum := runtime.SHA256(data)
	key := "hookship/v1/it/" + checksum.String()[:16] + "/final/ciphertext"

	put, err := client.PutImmutable(ctx, key, data, checksum)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if put.Version == "" {
		t.Fatal("expected a provider version from MinIO")
	}

	got, err := client.Get(ctx, key, checksum)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("round-trip mismatch: %q", got)
	}

	// Idempotent re-put of identical bytes must not overwrite.
	again, err := client.PutImmutable(ctx, key, data, checksum)
	if err != nil {
		t.Fatalf("idempotent put: %v", err)
	}
	if !again.AlreadyExisted {
		t.Fatal("re-put of identical bytes should report AlreadyExisted")
	}

	// Immutability: different bytes to the same key must be rejected.
	tampered := []byte("tampered-bytes")
	if _, err := client.PutImmutable(ctx, key, tampered, runtime.SHA256(tampered)); err != runtime.ErrPayloadObjectConflict {
		t.Fatalf("expected immutability conflict, got %v", err)
	}

	if err := client.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := client.Get(ctx, key, checksum); err != runtime.ErrPayloadObjectNotFound {
		t.Fatalf("expected not found after delete, got %v", err)
	}
}

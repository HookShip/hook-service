package assignmentconfig_test

import (
	"context"
	"testing"

	"github.com/hookship/hook-service/internal/assignmentconfig"
	"github.com/hookship/hook-service/internal/runtime"
)

const sampleYAML = `
backend: postgres
evaluationMode: false
payloadBackend: postgres
assignments:
  - id: asg_orders
    credentialId: cred_orders_01
    hmacSecret: hex:000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f
    environment: production
    routes: [orders.primary, orders.secondary]
    maxBodyBytes: 65536
    endpoint:
      id: ep_orders
      url: https://sink.example/hooks/orders
      signingSecret: hookship-assignmentconfig-signing-key
      configGeneration: 7
      maxAttempts: 10
      timeoutSeconds: 15
`

func TestParseAndConvert(t *testing.T) {
	file, err := assignmentconfig.Parse([]byte(sampleYAML))
	if err != nil {
		t.Fatal(err)
	}
	if file.Backend != "postgres" {
		t.Fatalf("backend = %q", file.Backend)
	}
	assignments, err := file.RuntimeAssignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 1 {
		t.Fatalf("expected one assignment, got %d", len(assignments))
	}
	assignment := assignments[0]
	if assignment.ID != "asg_orders" || assignment.CredentialID != "cred_orders_01" {
		t.Fatalf("unexpected identity: %+v", assignment)
	}
	if len(assignment.HMACSecret) != 32 {
		t.Fatalf("hmac secret length = %d", len(assignment.HMACSecret))
	}
	if !assignment.AllowsRoute("orders.primary") || assignment.AllowsRoute("unknown") {
		t.Fatal("route authorization incorrect")
	}
	if assignment.Endpoint.ConfigGeneration != 7 {
		t.Fatalf("generation = %d", assignment.Endpoint.ConfigGeneration)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := assignmentconfig.Parse([]byte("backend: memory\nbogusField: true\n"))
	if err == nil {
		t.Fatal("expected unknown field rejection")
	}
}

func TestRuntimeAssignmentsRejectsShortSecret(t *testing.T) {
	file := assignmentconfig.File{
		Assignments: []assignmentconfig.AssignmentSpec{{
			ID: "a", CredentialID: "c", HMACSecret: "short", Routes: []string{"r"},
			Endpoint: assignmentconfig.EndpointSpec{ID: "e", URL: "https://x.test", SigningSecret: "s"},
		}},
	}
	if _, err := file.RuntimeAssignments(); err == nil {
		t.Fatal("expected short secret rejection")
	}
}

func TestStoreResolvesAndLists(t *testing.T) {
	store, err := assignmentconfig.NewStore([]runtime.Assignment{
		assignmentconfig.DefaultEvaluationAssignment("http://127.0.0.1:9099/sink"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.ResolveCredential(context.Background(), assignmentconfig.EvaluationCredentialID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Environment != runtime.EnvironmentEvaluation {
		t.Fatalf("environment = %s", got.Environment)
	}
	if _, err := store.ResolveCredential(context.Background(), "cred_unknown"); err != runtime.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	all, err := store.Assignments(context.Background())
	if err != nil || len(all) != 1 {
		t.Fatalf("expected one assignment, got %d (%v)", len(all), err)
	}
}

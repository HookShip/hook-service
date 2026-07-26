package assignmentconfig

import (
	"encoding/hex"
	"time"

	"github.com/hookship/hook-service/internal/runtime"
)

// evaluationHMACSecretHex is the obvious, public, low-entropy 32-byte test
// secret (0x00..0x1f) also used by the protocol signing fixtures. It is test
// material and authenticates only loopback evaluation assignments.
const evaluationHMACSecretHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

// evaluationSigningSecret is a fixed, clearly non-production endpoint signing
// key for the evaluation endpoint. It is a plain, self-describing string used
// directly as the HMAC key (no provider "whsec_" prefix, so it is not
// secret-shaped) and is deterministic so demos and tests can verify the
// outbound signature. It authenticates only loopback evaluation assignments.
const evaluationSigningSecret = "hookship-evaluation-endpoint-signing-key"

// DefaultEvaluationAssignment builds the well-known loopback evaluation
// assignment that delivers to the supplied local sink URL. It never appears in a
// durable/production profile.
func DefaultEvaluationAssignment(sinkURL string) runtime.Assignment {
	secret, _ := hex.DecodeString(evaluationHMACSecretHex)
	return runtime.Assignment{
		ID:           "asg_evaluation_local",
		CredentialID: EvaluationCredentialID,
		HMACSecret:   secret,
		Environment:  runtime.EnvironmentEvaluation,
		Routes:       []string{"orders.primary", "evaluation.default"},
		MaxBodyBytes: 64 << 10,
		RetryHorizon: 24 * time.Hour,
		Endpoint: runtime.EndpointConfig{
			EndpointID:       "ep_evaluation_sink",
			BundleID:         "bundle_evaluation_local",
			ConfigGeneration: 1,
			URL:              sinkURL,
			SigningSecret:    []byte(evaluationSigningSecret),
			MaxAttempts:      6,
			Timeout:          10 * time.Second,
		},
	}
}

// DefaultEvaluationFile returns a File describing the default evaluation
// assignment, for `hookshipctl` scaffolding and documentation.
func DefaultEvaluationFile(sinkURL string) File {
	return File{
		Backend:        "memory",
		EvaluationMode: true,
		PayloadBackend: "memory",
		Assignments: []AssignmentSpec{
			{
				ID:           "asg_evaluation_local",
				CredentialID: EvaluationCredentialID,
				HMACSecret:   "hex:" + evaluationHMACSecretHex,
				Environment:  "evaluation",
				Routes:       []string{"orders.primary", "evaluation.default"},
				MaxBodyBytes: 64 << 10,
				Endpoint: EndpointSpec{
					ID:               "ep_evaluation_sink",
					URL:              sinkURL,
					SigningSecret:    evaluationSigningSecret,
					ConfigGeneration: 1,
					MaxAttempts:      6,
					TimeoutSeconds:   10,
				},
			},
		},
	}
}

// Package assignmentconfig loads the local assignment/credential source used by
// the memory and single-cell PostgreSQL profiles. It parses a hookship.yaml (or
// .json) file describing immutable assignments: credential, HMAC secret, routes,
// limits, and the delivery endpoint with its Standard Webhooks signing secret.
//
// The control plane is never required on the delivery hot path; these
// assignments are the local last-known-good configuration.
package assignmentconfig

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hookship/hook-service/internal/runtime"
)

// EvaluationCredentialID is the well-known, non-production credential used by
// the default evaluation assignment. It matches the protocol signing fixtures so
// the published vectors authenticate against a loopback evaluation server.
const EvaluationCredentialID = "cred_test_01"

// File is the on-disk assignment configuration.
type File struct {
	Backend        string           `yaml:"backend" json:"backend"`
	EvaluationMode bool             `yaml:"evaluationMode" json:"evaluationMode"`
	PayloadBackend string           `yaml:"payloadBackend" json:"payloadBackend"`
	Assignments    []AssignmentSpec `yaml:"assignments" json:"assignments"`
}

// AssignmentSpec is one immutable assignment declaration.
type AssignmentSpec struct {
	ID           string       `yaml:"id" json:"id"`
	CredentialID string       `yaml:"credentialId" json:"credentialId"`
	HMACSecret   string       `yaml:"hmacSecret" json:"hmacSecret"`
	Environment  string       `yaml:"environment" json:"environment"`
	Routes       []string     `yaml:"routes" json:"routes"`
	MaxBodyBytes int64        `yaml:"maxBodyBytes" json:"maxBodyBytes"`
	RetryHours   int          `yaml:"retryHorizonHours" json:"retryHorizonHours"`
	Endpoint     EndpointSpec `yaml:"endpoint" json:"endpoint"`
}

// EndpointSpec is the delivery destination and outbound signing material.
type EndpointSpec struct {
	ID               string `yaml:"id" json:"id"`
	URL              string `yaml:"url" json:"url"`
	SigningSecret    string `yaml:"signingSecret" json:"signingSecret"`
	ConfigGeneration int64  `yaml:"configGeneration" json:"configGeneration"`
	MaxAttempts      int    `yaml:"maxAttempts" json:"maxAttempts"`
	TimeoutSeconds   int    `yaml:"timeoutSeconds" json:"timeoutSeconds"`
}

// Load reads and parses an assignment configuration file. JSON is a subset of
// YAML, so a single YAML decoder handles both extensions.
func Load(path string) (File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("read assignment config: %w", err)
	}
	return Parse(data)
}

// Parse decodes assignment configuration bytes.
func Parse(data []byte) (File, error) {
	var file File
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return File{}, fmt.Errorf("decode assignment config: %w", err)
	}
	return file, nil
}

// RuntimeAssignments converts and validates the declared assignments into the
// runtime model, decoding secret material.
func (f File) RuntimeAssignments() ([]runtime.Assignment, error) {
	if len(f.Assignments) == 0 {
		return nil, errors.New("assignment config must declare at least one assignment")
	}
	seenCredentials := make(map[string]struct{}, len(f.Assignments))
	seenIDs := make(map[string]struct{}, len(f.Assignments))
	assignments := make([]runtime.Assignment, 0, len(f.Assignments))
	for index, spec := range f.Assignments {
		assignment, err := spec.toRuntime()
		if err != nil {
			return nil, fmt.Errorf("assignment %d (%s): %w", index, spec.ID, err)
		}
		if _, ok := seenCredentials[assignment.CredentialID]; ok {
			return nil, fmt.Errorf("duplicate credential ID %q", assignment.CredentialID)
		}
		if _, ok := seenIDs[assignment.ID]; ok {
			return nil, fmt.Errorf("duplicate assignment ID %q", assignment.ID)
		}
		seenCredentials[assignment.CredentialID] = struct{}{}
		seenIDs[assignment.ID] = struct{}{}
		assignments = append(assignments, assignment)
	}
	return assignments, nil
}

func (spec AssignmentSpec) toRuntime() (runtime.Assignment, error) {
	if spec.ID == "" || spec.CredentialID == "" {
		return runtime.Assignment{}, errors.New("id and credentialId are required")
	}
	if len(spec.Routes) == 0 {
		return runtime.Assignment{}, errors.New("at least one route is required")
	}
	hmacSecret, err := decodeSecret(spec.HMACSecret)
	if err != nil {
		return runtime.Assignment{}, fmt.Errorf("hmacSecret: %w", err)
	}
	if len(hmacSecret) < 16 {
		return runtime.Assignment{}, errors.New("hmacSecret must be at least 16 bytes")
	}
	signingSecret, err := decodeSecret(spec.Endpoint.SigningSecret)
	if err != nil {
		return runtime.Assignment{}, fmt.Errorf("endpoint.signingSecret: %w", err)
	}
	if len(signingSecret) == 0 {
		return runtime.Assignment{}, errors.New("endpoint.signingSecret is required")
	}
	if spec.Endpoint.URL == "" || spec.Endpoint.ID == "" {
		return runtime.Assignment{}, errors.New("endpoint id and url are required")
	}

	var environment runtime.Environment
	switch strings.ToLower(strings.TrimSpace(spec.Environment)) {
	case "", "production":
		environment = runtime.EnvironmentProduction
	case "evaluation":
		environment = runtime.EnvironmentEvaluation
	default:
		return runtime.Assignment{}, fmt.Errorf("unknown environment %q", spec.Environment)
	}

	maxBody := spec.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = 256 << 10
	}
	retry := time.Duration(spec.RetryHours) * time.Hour
	if retry < 24*time.Hour {
		retry = 24 * time.Hour
	}
	maxAttempts := spec.Endpoint.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 12
	}
	timeout := time.Duration(spec.Endpoint.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	generation := spec.Endpoint.ConfigGeneration
	if generation <= 0 {
		generation = 1
	}

	return runtime.Assignment{
		ID:           spec.ID,
		CredentialID: spec.CredentialID,
		HMACSecret:   hmacSecret,
		Environment:  environment,
		Routes:       spec.Routes,
		MaxBodyBytes: maxBody,
		RetryHorizon: retry,
		Endpoint: runtime.EndpointConfig{
			EndpointID:       spec.Endpoint.ID,
			BundleID:         "bundle_" + spec.ID,
			ConfigGeneration: generation,
			URL:              spec.Endpoint.URL,
			SigningSecret:    signingSecret,
			MaxAttempts:      maxAttempts,
			Timeout:          timeout,
		},
	}, nil
}

// decodeSecret accepts an explicit `hex:` / `base64:` prefix, a Standard
// Webhooks `whsec_` secret (returned verbatim for the signer to decode), or raw
// UTF-8 bytes.
func decodeSecret(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	switch {
	case strings.HasPrefix(value, "hex:"):
		return hex.DecodeString(strings.TrimPrefix(value, "hex:"))
	case strings.HasPrefix(value, "base64:"):
		return base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "base64:"))
	default:
		return []byte(value), nil
	}
}

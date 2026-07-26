// Package diagnostics probes the hookshipd operational endpoints.
package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maximumResponseBytes = 32 << 10

// ProbeResult captures one bounded operational endpoint check.
type ProbeResult struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Status     string `json:"status,omitempty"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

// Report captures health and readiness at a target.
type Report struct {
	Target    string      `json:"target"`
	Health    ProbeResult `json:"health"`
	Readiness ProbeResult `json:"readiness"`
	OK        bool        `json:"ok"`
}

// Run probes health and readiness with the supplied client.
func Run(ctx context.Context, client *http.Client, baseURL, userAgent string) (Report, error) {
	if client == nil {
		return Report{}, errors.New("HTTP client is required")
	}
	normalized, err := normalizeBaseURL(baseURL)
	if err != nil {
		return Report{}, err
	}

	report := Report{
		Target: normalized.String(),
		Health: probe(ctx, client, normalized, "health", "/healthz", "ok", userAgent),
		Readiness: probe(
			ctx,
			client,
			normalized,
			"readiness",
			"/readyz",
			"ready",
			userAgent,
		),
	}
	report.OK = report.Health.OK && report.Readiness.OK
	return report, nil
}

func probe(
	ctx context.Context,
	client *http.Client,
	baseURL *url.URL,
	name string,
	path string,
	expectedStatus string,
	userAgent string,
) ProbeResult {
	endpoint := *baseURL
	endpoint.Path = path
	result := ProbeResult{Name: name, URL: endpoint.String()}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	request.Header.Set("Accept", "application/json")
	if userAgent != "" {
		request.Header.Set("User-Agent", userAgent)
	}

	response, err := client.Do(request)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer func() { _ = response.Body.Close() }()
	result.HTTPStatus = response.StatusCode

	var body struct {
		Status string `json:"status"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maximumResponseBytes))
	if err := decoder.Decode(&body); err != nil {
		result.Error = fmt.Sprintf("decode response: %v", err)
		return result
	}
	result.Status = body.Status
	result.OK = response.StatusCode >= 200 &&
		response.StatusCode < 300 &&
		body.Status == expectedStatus
	if !result.OK {
		result.Error = fmt.Sprintf("expected HTTP 2xx with status %q", expectedStatus)
	}
	return result
}

func normalizeBaseURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("parse diagnostics URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("diagnostics URL scheme must be http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("diagnostics URL must include a host")
	}
	if parsed.User != nil {
		return nil, errors.New("diagnostics URL must not include user information")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("diagnostics URL must not include a query or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("diagnostics URL must not include a path")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, nil
}

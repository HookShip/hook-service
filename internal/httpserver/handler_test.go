package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hookship/hook-service/internal/buildinfo"
)

func TestHealthAndReadiness(t *testing.T) {
	t.Parallel()

	readiness := &Readiness{}
	handler := newHandler(readiness, buildinfo.Info{Version: "test"})

	assertStatus(t, handler, http.MethodGet, "/healthz", http.StatusOK, "ok")
	assertStatus(t, handler, http.MethodGet, "/readyz", http.StatusServiceUnavailable, "not_ready")

	readiness.Set(true)
	assertStatus(t, handler, http.MethodGet, "/readyz", http.StatusOK, "ready")
}

func TestStatusEndpointMethods(t *testing.T) {
	t.Parallel()

	readiness := &Readiness{}
	readiness.Set(true)
	handler := newHandler(readiness, buildinfo.Info{Version: "test"})

	request := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q", got)
	}

	request = httptest.NewRequest(http.MethodHead, "/healthz", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("HEAD status code = %d", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("HEAD response body = %q", recorder.Body.String())
	}
}

func TestUnknownEndpointIsNotFound(t *testing.T) {
	t.Parallel()

	handler := newHandler(&Readiness{}, buildinfo.Info{Version: "test"})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status code = %d", recorder.Code)
	}
}

func assertStatus(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	wantCode int,
	wantStatus string,
) {
	t.Helper()

	request := httptest.NewRequest(method, path, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != wantCode {
		t.Fatalf("%s %s status code = %d, want %d", method, path, recorder.Code, wantCode)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var response statusResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Status != wantStatus {
		t.Fatalf("status = %q, want %q", response.Status, wantStatus)
	}
	if response.Service != "hookshipd" {
		t.Fatalf("service = %q", response.Service)
	}
}

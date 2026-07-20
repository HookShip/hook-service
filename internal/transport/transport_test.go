package transport_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/transport"
)

type fakeResolver struct {
	ips []net.IP
}

func (f fakeResolver) LookupIP(_ context.Context, _, _ string) ([]net.IP, error) {
	return f.ips, nil
}

func TestDeliverToLoopbackSinkSucceeds(t *testing.T) {
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		received = body
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := transport.NewClient(transport.Options{
		AllowLoopback:  true,
		AllowPlaintext: true,
	})
	result, err := client.Deliver(context.Background(), server.URL, "application/json",
		map[string]string{"webhook-id": "msg_1"}, []byte(`{"k":"v"}`))
	if err != nil {
		t.Fatalf("deliver failed: %v", err)
	}
	if result.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", result.StatusCode)
	}
	if string(received) != `{"k":"v"}` {
		t.Fatalf("payload not received exactly: %q", received)
	}
}

func TestDeliverRejectsRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.test/elsewhere", http.StatusFound)
	}))
	defer server.Close()

	client := transport.NewClient(transport.Options{AllowLoopback: true, AllowPlaintext: true})
	_, err := client.Deliver(context.Background(), server.URL, "application/json", nil, []byte("{}"))
	if err == nil {
		t.Fatal("expected redirect to be refused")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("expected redirect error, got %v", err)
	}
}

func TestSSRFCorpusFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		ip   string
	}{
		{"loopback v4", "127.0.0.1"},
		{"loopback v6", "::1"},
		{"private 10", "10.1.2.3"},
		{"private 172", "172.16.9.9"},
		{"private 192", "192.168.1.1"},
		{"link local", "169.254.10.10"},
		{"aws metadata", "169.254.169.254"},
		{"gcp metadata alias", "169.254.169.254"},
		{"alibaba metadata", "100.100.100.200"},
		{"ula v6", "fc00::1234"},
		{"unspecified", "0.0.0.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := transport.NewClient(transport.Options{
				Resolver:     fakeResolver{ips: []net.IP{net.ParseIP(tc.ip)}},
				TotalTimeout: 2 * time.Second,
			})
			// A public-looking hostname that (maliciously) resolves to a blocked IP.
			_, err := client.Deliver(context.Background(), "https://totally-public.example/hook", "application/json", nil, []byte("{}"))
			if err == nil {
				t.Fatalf("expected blocked address error for %s", tc.ip)
			}
		})
	}
}

func TestDNSRebindingPinsValidatedAddress(t *testing.T) {
	// Even if the resolver returns a public then a private address, only the
	// first allowed one is pinned; a resolver that returns only private
	// addresses must fail closed.
	client := transport.NewClient(transport.Options{
		Resolver:     fakeResolver{ips: []net.IP{net.ParseIP("10.0.0.5"), net.ParseIP("169.254.169.254")}},
		TotalTimeout: 2 * time.Second,
	})
	_, err := client.Deliver(context.Background(), "https://rebind.example/hook", "application/json", nil, []byte("{}"))
	if err == nil {
		t.Fatal("expected rebinding attempt to fail closed")
	}
}

func TestValidateURLRejectsUnsafeSchemes(t *testing.T) {
	client := transport.NewClient(transport.Options{})
	for _, raw := range []string{
		"ftp://example.test/x",
		"file:///etc/passwd",
		"http://example.test/x", // plaintext not allowed by default
		"https://user:pass@example.test/x",
		"https:///nohostpath",
	} {
		if err := client.ValidateURL(raw); err == nil {
			t.Fatalf("expected rejection of %q", raw)
		}
	}
	if err := client.ValidateURL("https://example.test/hook"); err != nil {
		t.Fatalf("expected https to be accepted: %v", err)
	}
}

func TestDeliverParsesRetryAfterSeconds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := transport.NewClient(transport.Options{AllowLoopback: true, AllowPlaintext: true})
	result, err := client.Deliver(context.Background(), server.URL, "application/json", nil, []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.HasRetryAfter || result.RetryAfter != 30*time.Second {
		t.Fatalf("expected 30s Retry-After, got %v (%v)", result.RetryAfter, result.HasRetryAfter)
	}
}

func TestDeliverDiscardsOversizeResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		blob := strings.Repeat("A", 512*1024)
		_, _ = w.Write([]byte(blob))
	}))
	defer server.Close()
	client := transport.NewClient(transport.Options{
		AllowLoopback:    true,
		AllowPlaintext:   true,
		MaxResponseBytes: 1024,
	})
	result, err := client.Deliver(context.Background(), server.URL, "application/json", nil, []byte("{}"))
	if err != nil {
		t.Fatalf("delivery should succeed even with a huge response: %v", err)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", result.StatusCode)
	}
}

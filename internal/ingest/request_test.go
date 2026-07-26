package ingest

import (
	"net/http"
	"testing"
)

func TestSingleHeader(t *testing.T) {
	header := http.Header{}
	header.Set("X-One", "value")
	header["X-Dup"] = []string{"a", "b"}

	value, present, duplicate := singleHeader(header, "X-One")
	if !present || duplicate || value != "value" {
		t.Fatalf("single header = %q present=%v duplicate=%v", value, present, duplicate)
	}
	_, present, _ = singleHeader(header, "X-Absent")
	if present {
		t.Fatal("absent header must report not present")
	}
	_, _, duplicate = singleHeader(header, "X-Dup")
	if !duplicate {
		t.Fatal("duplicated header must report duplicate")
	}
}

func TestValidEventHeaders(t *testing.T) {
	valid := signedHeaderSet{
		eventID:     "evt_1",
		eventType:   "order.created",
		route:       "orders.primary",
		contentType: "application/json",
	}
	if !validEventHeaders(valid) {
		t.Fatal("valid headers rejected")
	}

	cases := map[string]func(signedHeaderSet) signedHeaderSet{
		"bad event id":   func(h signedHeaderSet) signedHeaderSet { h.eventID = "bad id!"; return h },
		"bad event type": func(h signedHeaderSet) signedHeaderSet { h.eventType = "Bad.Type"; return h },
		"bad route":      func(h signedHeaderSet) signedHeaderSet { h.route = "Bad Route"; return h },
		"short ct":       func(h signedHeaderSet) signedHeaderSet { h.contentType = "a"; return h },
		"newline ct":     func(h signedHeaderSet) signedHeaderSet { h.contentType = "application/json\r\n"; return h },
	}
	for name, mutate := range cases {
		if validEventHeaders(mutate(valid)) {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

func TestItoa(t *testing.T) {
	cases := map[int64]string{0: "0", 1: "1", 42: "42", 65536: "65536"}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Fatalf("itoa(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestIsLoopbackRequest(t *testing.T) {
	loopback := &http.Request{RemoteAddr: "127.0.0.1:5555"}
	if !isLoopbackRequest(loopback) {
		t.Fatal("127.0.0.1 must be loopback")
	}
	remote := &http.Request{RemoteAddr: "203.0.113.7:443"}
	if isLoopbackRequest(remote) {
		t.Fatal("public address must not be loopback")
	}
}

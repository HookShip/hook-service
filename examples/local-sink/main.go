// Command local-sink is a minimal webhook receiver for the evaluation demo. It
// receives Standard Webhooks deliveries, optionally verifies the signature with
// the evaluation signing secret, logs a payload-free summary, and can fail the
// first N attempts to demonstrate retry and DLQ behavior.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// evaluationSigningSecret mirrors the fixed evaluation endpoint signing key used
// by the default evaluation assignment. It is a plain, clearly non-provider
// string (no "whsec_" prefix) used directly as the HMAC key. Public test
// material only.
const evaluationSigningSecret = "hookship-evaluation-endpoint-signing-key"

func main() {
	addr := flag.String("addr", "127.0.0.1:9099", "listen address")
	failFirst := flag.Int("fail-first", envInt("SINK_FAIL_FIRST", 0), "return HTTP 500 for the first N deliveries (demonstrates retry)")
	flag.Parse()

	var count atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/sink", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		id := r.Header.Get("webhook-id")
		ts := r.Header.Get("webhook-timestamp")
		sig := r.Header.Get("webhook-signature")
		verified := verify(id, ts, sig, body)

		n := count.Add(1)
		if int(n) <= *failFirst {
			log.Printf("delivery #%d id=%s verified=%t -> returning 500 (fail-first)", n, id, verified)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		log.Printf("delivery #%d id=%s verified=%t bytes=%d -> 204", n, id, verified, len(body))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("local-sink listening on %s (fail-first=%d)", *addr, *failFirst)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func verify(id, ts, header string, body []byte) bool {
	key, err := decodeSecret(evaluationSigningSecret)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id))
	mac.Write([]byte("."))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := mac.Sum(nil)
	for _, candidate := range strings.Fields(header) {
		parts := strings.SplitN(candidate, ",", 2)
		if len(parts) != 2 || parts[0] != "v1" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		if hmac.Equal(expected, decoded) {
			return true
		}
	}
	return false
}

// decodeSecret resolves the raw HMAC key from a Standard Webhooks secret,
// mirroring the production signer: a "whsec_" prefix selects base64 decoding of
// the remainder; otherwise the raw bytes are the key.
func decodeSecret(secret string) ([]byte, error) {
	if strings.HasPrefix(secret, "whsec_") {
		return base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	}
	return []byte(secret), nil
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

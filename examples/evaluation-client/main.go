// Command evaluation-client is a reference protocol-v1 producer for the
// evaluation profile. It signs an event with the well-known evaluation
// credential and submits it to a loopback hookshipd, then reads payload-free
// status. It exists only to demonstrate the wire protocol; the evaluation
// secret it uses is public test material and authenticates only loopback
// evaluation assignments.
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	credentialID = "cred_test_01"
	secretHex    = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
)

func main() {
	base := flag.String("url", "http://127.0.0.1:8080", "hookshipd base URL")
	route := flag.String("route", "orders.primary", "assignment-authorized route")
	eventType := flag.String("event-type", "order.created", "event type")
	idempotencyKey := flag.String("idempotency-key", "", "idempotency key (default: time-based)")
	bodyText := flag.String("body", `{"order_id":"123","amount":42}`, "raw request body")
	flag.Parse()

	if *idempotencyKey == "" {
		*idempotencyKey = fmt.Sprintf("op-%d", time.Now().UnixNano())
	}
	secret, _ := hex.DecodeString(secretHex)
	body := []byte(*bodyText)
	bodyHash := sha256.Sum256(body)
	bodyHashHex := hex.EncodeToString(bodyHash[:])
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	nonce := freshNonce()
	eventID := "evt_" + freshToken()

	canonical := strings.Join([]string{
		"hookship-hmac-sha256-v1",
		"method:POST",
		"path:/v1/event-ingests",
		"credential-id:" + credentialID,
		"timestamp:" + timestamp,
		"nonce:" + nonce,
		"idempotency-key:" + *idempotencyKey,
		"event-id:" + eventID,
		"event-type:" + *eventType,
		"route:" + *route,
		"content-type:application/json",
		"body-sha256:" + bodyHashHex,
	}, "\n")
	signature := signBase64URL(secret, canonical)

	request, err := http.NewRequest(http.MethodPost, *base+"/v1/event-ingests", bytes.NewReader(body))
	if err != nil {
		fail(err)
	}
	request.Header.Set("Authorization", "Hookship-HMAC Credential="+credentialID+",Signature="+signature)
	request.Header.Set("X-Hookship-Timestamp", timestamp)
	request.Header.Set("X-Hookship-Nonce", nonce)
	request.Header.Set("Idempotency-Key", *idempotencyKey)
	request.Header.Set("X-Hookship-Event-ID", eventID)
	request.Header.Set("X-Hookship-Event-Type", *eventType)
	request.Header.Set("X-Hookship-Route", *route)
	request.Header.Set("X-Hookship-Body-SHA256", bodyHashHex)
	request.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		fail(err)
	}
	receipt := readJSON(response)
	fmt.Printf("POST /v1/event-ingests -> %d\n", response.StatusCode)
	printJSON(receipt)

	ingestID, _ := receipt["ingestId"].(string)
	if response.StatusCode >= 300 || ingestID == "" {
		os.Exit(1)
	}

	// Poll payload-free status.
	for attempt := 0; attempt < 10; attempt++ {
		time.Sleep(500 * time.Millisecond)
		status := getStatus(*base, secret, ingestID)
		state, _ := status["dispatchState"].(string)
		fmt.Printf("GET status -> dispatchState=%s\n", state)
		if state == "completed" || state == "failed" {
			printJSON(status)
			return
		}
	}
	fmt.Println("status did not reach a terminal dispatch state within the poll window")
}

func getStatus(base string, secret []byte, ingestID string) map[string]any {
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	nonce := freshNonce()
	emptyHash := sha256.Sum256(nil)
	emptyHex := hex.EncodeToString(emptyHash[:])
	canonical := strings.Join([]string{
		"hookship-hmac-sha256-v1",
		"method:GET",
		"path:/v1/event-ingests/" + ingestID,
		"credential-id:" + credentialID,
		"timestamp:" + timestamp,
		"nonce:" + nonce,
		"idempotency-key:",
		"event-id:",
		"event-type:",
		"route:",
		"content-type:",
		"body-sha256:" + emptyHex,
	}, "\n")
	signature := signBase64URL(secret, canonical)
	request, _ := http.NewRequest(http.MethodGet, base+"/v1/event-ingests/"+ingestID, nil)
	request.Header.Set("Authorization", "Hookship-HMAC Credential="+credentialID+",Signature="+signature)
	request.Header.Set("X-Hookship-Timestamp", timestamp)
	request.Header.Set("X-Hookship-Nonce", nonce)
	request.Header.Set("X-Hookship-Body-SHA256", emptyHex)
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		fail(err)
	}
	return readJSON(response)
}

func signBase64URL(secret []byte, canonical string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func freshNonce() string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("nonce-%d", time.Now().UnixNano())))
}

func freshToken() string {
	return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))[:16]
}

func readJSON(response *http.Response) map[string]any {
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	var parsed map[string]any
	_ = json.Unmarshal(data, &parsed)
	return parsed
}

func printJSON(value any) {
	encoded, _ := json.MarshalIndent(value, "", "  ")
	fmt.Println(string(encoded))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "evaluation-client:", err)
	os.Exit(1)
}

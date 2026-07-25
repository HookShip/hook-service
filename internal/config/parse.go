package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hookship/hook-service/internal/payloadcrypto"
)

func decodeKey(value string) ([]byte, error) {
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == payloadcrypto.KeySize {
		return decoded, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) == payloadcrypto.KeySize {
		return decoded, nil
	}
	return nil, fmt.Errorf("%s must be a %d-byte key in hex or base64", envPayloadKey, payloadcrypto.KeySize)
}

// envParser parses a single HOOKSHIP_* variable into a typed destination,
// leaving the destination untouched (its default) when the variable is unset.
type envParser func(getenv func(string) string) error

func boolEnv(name string, dst *bool) envParser {
	return func(getenv func(string) string) error {
		parsed, err := boolFromEnv(getenv, name)
		if err != nil {
			return err
		}
		*dst = parsed
		return nil
	}
}

func intEnv(name string, dst *int) envParser {
	return func(getenv func(string) string) error { return intFromEnv(getenv, name, dst) }
}

func floatEnv(name string, dst *float64) envParser {
	return func(getenv func(string) string) error { return floatFromEnv(getenv, name, dst) }
}

func durationEnv(name string, dst *time.Duration) envParser {
	return func(getenv func(string) string) error { return durationFromEnv(getenv, name, dst) }
}

func boolFromEnv(getenv func(string) string, name string) (bool, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", name, err)
	}
	return parsed, nil
}

func intFromEnv(getenv func(string) string, name string, destination *int) error {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%s must be an integer: %w", name, err)
	}
	*destination = parsed
	return nil
}

func floatFromEnv(getenv func(string) string, name string, destination *float64) error {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("%s must be a number: %w", name, err)
	}
	if parsed < 0 {
		return fmt.Errorf("%s must not be negative", name)
	}
	*destination = parsed
	return nil
}

// isLoopbackBind reports whether a listen address binds only the loopback
// interface.
func isLoopbackBind(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	host = strings.TrimSpace(host)
	switch host {
	case "localhost":
		return true
	case "":
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isLoopbackEndpoint reports whether an object-store endpoint URL or host:port
// targets the loopback interface, so local MinIO over plaintext HTTP is allowed
// while a remote plaintext endpoint fails closed in production.
func isLoopbackEndpoint(endpoint string) bool {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return false
	}
	host := endpoint
	if strings.Contains(endpoint, "://") {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return false
		}
		host = parsed.Host
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

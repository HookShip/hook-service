// Package config loads and validates hookshipd process configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListenAddress   = "127.0.0.1:8080"
	defaultShutdownTimeout = 10 * time.Second
	maximumShutdownTimeout = 5 * time.Minute
	defaultReadHeader      = 5 * time.Second
	maximumReadHeader      = 1 * time.Minute
	defaultReadTimeout     = 10 * time.Second
	maximumReadTimeout     = 10 * time.Minute
	defaultWriteTimeout    = 10 * time.Second
	maximumWriteTimeout    = 10 * time.Minute
	defaultIdleTimeout     = 60 * time.Second
	maximumIdleTimeout     = 1 * time.Hour
	defaultMaxHeaderBytes  = 64 << 10
	minimumMaxHeaderBytes  = 4 << 10
	maximumMaxHeaderBytes  = 1 << 20
	defaultLogLevel        = "info"
	envListenAddress       = "HOOKSHIP_LISTEN_ADDRESS"
	envShutdownTimeout     = "HOOKSHIP_SHUTDOWN_TIMEOUT"
	envReadHeaderTimeout   = "HOOKSHIP_READ_HEADER_TIMEOUT"
	envReadTimeout         = "HOOKSHIP_READ_TIMEOUT"
	envWriteTimeout        = "HOOKSHIP_WRITE_TIMEOUT"
	envIdleTimeout         = "HOOKSHIP_IDLE_TIMEOUT"
	envMaxHeaderBytes      = "HOOKSHIP_MAX_HEADER_BYTES"
	envLogLevel            = "HOOKSHIP_LOG_LEVEL"
)

// Config contains the bounded settings needed by the HTTP process.
type Config struct {
	ListenAddress   string
	ShutdownTimeout time.Duration
	ReadHeader      time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	MaxHeaderBytes  int
	LogLevel        string
}

// Default returns a configuration that only listens on the local loopback.
func Default() Config {
	return Config{
		ListenAddress:   defaultListenAddress,
		ShutdownTimeout: defaultShutdownTimeout,
		ReadHeader:      defaultReadHeader,
		ReadTimeout:     defaultReadTimeout,
		WriteTimeout:    defaultWriteTimeout,
		IdleTimeout:     defaultIdleTimeout,
		MaxHeaderBytes:  defaultMaxHeaderBytes,
		LogLevel:        defaultLogLevel,
	}
}

// FromEnv overlays supported environment variables on the secure defaults.
func FromEnv(getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, errors.New("environment reader is required")
	}

	cfg := Default()
	if value := strings.TrimSpace(getenv(envListenAddress)); value != "" {
		cfg.ListenAddress = value
	}
	if err := durationFromEnv(getenv, envShutdownTimeout, &cfg.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if err := durationFromEnv(getenv, envReadHeaderTimeout, &cfg.ReadHeader); err != nil {
		return Config{}, err
	}
	if err := durationFromEnv(getenv, envReadTimeout, &cfg.ReadTimeout); err != nil {
		return Config{}, err
	}
	if err := durationFromEnv(getenv, envWriteTimeout, &cfg.WriteTimeout); err != nil {
		return Config{}, err
	}
	if err := durationFromEnv(getenv, envIdleTimeout, &cfg.IdleTimeout); err != nil {
		return Config{}, err
	}
	if value := strings.TrimSpace(getenv(envMaxHeaderBytes)); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s must be an integer: %w", envMaxHeaderBytes, err)
		}
		cfg.MaxHeaderBytes = parsed
	}
	if value := strings.TrimSpace(getenv(envLogLevel)); value != "" {
		cfg.LogLevel = strings.ToLower(value)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects unsafe or unusable process settings.
func (c Config) Validate() error {
	host, port, err := net.SplitHostPort(c.ListenAddress)
	if err != nil {
		return fmt.Errorf("%s must be a host:port address: %w", envListenAddress, err)
	}
	if strings.ContainsAny(host, "\r\n") {
		return fmt.Errorf("%s contains invalid characters", envListenAddress)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return fmt.Errorf("%s port must be between 0 and 65535", envListenAddress)
	}

	durations := []struct {
		name  string
		value time.Duration
		max   time.Duration
	}{
		{envShutdownTimeout, c.ShutdownTimeout, maximumShutdownTimeout},
		{envReadHeaderTimeout, c.ReadHeader, maximumReadHeader},
		{envReadTimeout, c.ReadTimeout, maximumReadTimeout},
		{envWriteTimeout, c.WriteTimeout, maximumWriteTimeout},
		{envIdleTimeout, c.IdleTimeout, maximumIdleTimeout},
	}
	for _, setting := range durations {
		if setting.value <= 0 {
			return fmt.Errorf("%s must be greater than zero", setting.name)
		}
		if setting.value > setting.max {
			return fmt.Errorf("%s must not exceed %s", setting.name, setting.max)
		}
	}

	if c.MaxHeaderBytes < minimumMaxHeaderBytes || c.MaxHeaderBytes > maximumMaxHeaderBytes {
		return fmt.Errorf(
			"%s must be between %d and %d",
			envMaxHeaderBytes,
			minimumMaxHeaderBytes,
			maximumMaxHeaderBytes,
		)
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("%s must be one of debug, info, warn, or error", envLogLevel)
	}
	return nil
}

func durationFromEnv(getenv func(string) string, name string, destination *time.Duration) error {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s must be a Go duration: %w", name, err)
	}
	*destination = parsed
	return nil
}

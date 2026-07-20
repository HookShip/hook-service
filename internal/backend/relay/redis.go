package relay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	redisStreamField   = "signal"
	redisDefaultStream = "hookship:dispatch"
	redisDefaultGroup  = "hookship-workers"
)

// RedisConfig configures the Redis Streams relay.
type RedisConfig struct {
	URL      string
	Stream   string
	Group    string
	Consumer string
	// MaxLen bounds the stream length (approximate) so it cannot grow unbounded.
	MaxLen int64
}

func (c RedisConfig) withDefaults() RedisConfig {
	if c.Stream == "" {
		c.Stream = redisDefaultStream
	}
	if c.Group == "" {
		c.Group = redisDefaultGroup
	}
	if c.MaxLen <= 0 {
		c.MaxLen = 100000
	}
	return c
}

// RedisPublisher publishes dispatch signals to a Redis Stream via XADD.
type RedisPublisher struct {
	client *redis.Client
	config RedisConfig
}

// NewRedisPublisher connects a Redis publisher.
func NewRedisPublisher(config RedisConfig) (*RedisPublisher, error) {
	config = config.withDefaults()
	options, err := redis.ParseURL(config.URL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return &RedisPublisher{client: redis.NewClient(options), config: config}, nil
}

// Publish appends a signal to the stream.
func (p *RedisPublisher) Publish(ctx context.Context, signal Signal) error {
	encoded, err := Encode(signal)
	if err != nil {
		return err
	}
	return p.client.XAdd(ctx, &redis.XAddArgs{
		Stream: p.config.Stream,
		Approx: true,
		MaxLen: p.config.MaxLen,
		Values: map[string]any{redisStreamField: encoded},
	}).Err()
}

// Close releases the Redis connection.
func (p *RedisPublisher) Close() error { return p.client.Close() }

// RedisConsumer consumes dispatch signals from a Redis Stream consumer group.
type RedisConsumer struct {
	client *redis.Client
	config RedisConfig
}

// NewRedisConsumer connects a Redis consumer and ensures the consumer group
// exists.
func NewRedisConsumer(ctx context.Context, config RedisConfig) (*RedisConsumer, error) {
	config = config.withDefaults()
	if config.Consumer == "" {
		return nil, errors.New("redis consumer name is required")
	}
	options, err := redis.ParseURL(config.URL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	client := redis.NewClient(options)
	if err := client.XGroupCreateMkStream(ctx, config.Stream, config.Group, "0").Err(); err != nil {
		if !isBusyGroup(err) {
			_ = client.Close()
			return nil, fmt.Errorf("create consumer group: %w", err)
		}
	}
	return &RedisConsumer{client: client, config: config}, nil
}

// Consume reads and acknowledges signals until the context is canceled.
func (c *RedisConsumer) Consume(ctx context.Context, handle func(context.Context, Signal) error) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		streams, err := c.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    c.config.Group,
			Consumer: c.config.Consumer,
			Streams:  []string{c.config.Stream, ">"},
			Count:    64,
			Block:    time.Second,
		}).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read consumer group: %w", err)
		}
		for _, stream := range streams {
			for _, message := range stream.Messages {
				c.handleMessage(ctx, message, handle)
			}
		}
	}
}

func (c *RedisConsumer) handleMessage(ctx context.Context, message redis.XMessage, handle func(context.Context, Signal) error) {
	raw, ok := message.Values[redisStreamField].(string)
	if ok {
		if signal, err := Decode([]byte(raw)); err == nil {
			_ = handle(ctx, signal)
		}
	}
	// Acknowledge regardless: a malformed or already-handled signal is advisory
	// only, and the durable job remains authoritative in PostgreSQL.
	_ = c.client.XAck(ctx, c.config.Stream, c.config.Group, message.ID).Err()
}

// Close releases the Redis connection.
func (c *RedisConsumer) Close() error { return c.client.Close() }

func isBusyGroup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BUSYGROUP")
}

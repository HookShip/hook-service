//go:build integration

package relay_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"github.com/hookship/hook-service/internal/backend/relay"
)

func randomSuffix(t *testing.T) string {
	t.Helper()
	value := make([]byte, 6)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value)
}

func TestRedisRelayRoundTrip(t *testing.T) {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Skip("REDIS_URL is not set")
	}
	suffix := randomSuffix(t)
	config := relay.RedisConfig{
		URL:      url,
		Stream:   "hookship:test:" + suffix,
		Group:    "grp-" + suffix,
		Consumer: "consumer-" + suffix,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	consumer, err := relay.NewRedisConsumer(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	publisher, err := relay.NewRedisPublisher(config)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()

	received := make(chan relay.Signal, 4)
	go func() {
		_ = consumer.Consume(ctx, func(_ context.Context, signal relay.Signal) error {
			received <- signal
			return nil
		})
	}()

	sent := relay.NewSignal("asg_redis", "job_"+suffix, "snap_redis", time.Now().UnixMilli())
	if err := publisher.Publish(ctx, sent); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got.JobID != sent.JobID {
			t.Fatalf("received job %q, want %q", got.JobID, sent.JobID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("did not receive published signal via Redis Streams")
	}
}

func TestKafkaRelayRoundTrip(t *testing.T) {
	brokersEnv := os.Getenv("KAFKA_BROKERS")
	if brokersEnv == "" {
		t.Skip("KAFKA_BROKERS is not set")
	}
	brokers := strings.Split(brokersEnv, ",")
	suffix := randomSuffix(t)
	topic := "hookship.test." + suffix

	// Create the topic explicitly for deterministic partitioning.
	conn, err := kafka.Dial("tcp", brokers[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.CreateTopics(kafka.TopicConfig{Topic: topic, NumPartitions: 3, ReplicationFactor: 1}); err != nil {
		t.Fatalf("create topic: %v", err)
	}
	_ = conn.Close()
	time.Sleep(time.Second)

	config := relay.KafkaConfig{Brokers: brokers, Topic: topic, GroupID: "grp-" + suffix}
	publisher, err := relay.NewKafkaPublisher(config)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	consumer, err := relay.NewKafkaConsumer(config)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var once sync.Once
	received := make(chan relay.Signal, 4)
	go func() {
		_ = consumer.Consume(ctx, func(_ context.Context, signal relay.Signal) error {
			once.Do(func() { received <- signal })
			return nil
		})
	}()

	// Publish repeatedly until the group is assigned and a message is consumed.
	sent := relay.NewSignal("asg_kafka", "job_"+suffix, "snap_kafka", time.Now().UnixMilli())
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if err := publisher.Publish(ctx, sent); err != nil {
			t.Fatalf("publish: %v", err)
		}
		select {
		case got := <-received:
			if got.JobID != sent.JobID {
				t.Fatalf("received job %q, want %q", got.JobID, sent.JobID)
			}
			return
		case <-time.After(2 * time.Second):
		}
	}
	t.Fatal("did not receive published signal via Kafka")
}

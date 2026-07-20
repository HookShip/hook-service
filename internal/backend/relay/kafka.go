package relay

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"
)

const kafkaDefaultTopic = "hookship.dispatch"

// KafkaConfig configures the Kafka dispatch relay.
type KafkaConfig struct {
	Brokers []string
	Topic   string
	GroupID string
}

func (c KafkaConfig) withDefaults() KafkaConfig {
	if c.Topic == "" {
		c.Topic = kafkaDefaultTopic
	}
	if c.GroupID == "" {
		c.GroupID = "hookship-workers"
	}
	return c
}

// KafkaPublisher publishes dispatch signals to Kafka, keyed by endpoint so that
// one endpoint's signals stay on a single partition (partition-scoped ordering).
type KafkaPublisher struct {
	writer *kafka.Writer
}

// NewKafkaPublisher builds a Kafka publisher.
func NewKafkaPublisher(config KafkaConfig) (*KafkaPublisher, error) {
	config = config.withDefaults()
	if len(config.Brokers) == 0 {
		return nil, errors.New("kafka brokers are required")
	}
	writer := &kafka.Writer{
		Addr:         kafka.TCP(config.Brokers...),
		Topic:        config.Topic,
		Balancer:     &kafka.Hash{}, // partition by key
		RequiredAcks: kafka.RequireAll,
		BatchTimeout: 20 * time.Millisecond,
	}
	return &KafkaPublisher{writer: writer}, nil
}

// Publish writes a signal keyed by its partition key.
func (p *KafkaPublisher) Publish(ctx context.Context, signal Signal) error {
	encoded, err := Encode(signal)
	if err != nil {
		return err
	}
	return p.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(signal.PartitionKey()),
		Value: encoded,
	})
}

// Close flushes and closes the writer.
func (p *KafkaPublisher) Close() error { return p.writer.Close() }

// KafkaConsumer consumes dispatch signals from a Kafka consumer group.
type KafkaConsumer struct {
	reader *kafka.Reader
}

// NewKafkaConsumer builds a Kafka consumer bound to a group.
func NewKafkaConsumer(config KafkaConfig) (*KafkaConsumer, error) {
	config = config.withDefaults()
	if len(config.Brokers) == 0 {
		return nil, errors.New("kafka brokers are required")
	}
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        config.Brokers,
		Topic:          config.Topic,
		GroupID:        config.GroupID,
		MinBytes:       1,
		MaxBytes:       1 << 20,
		MaxWait:        500 * time.Millisecond,
		CommitInterval: time.Second,
	})
	return &KafkaConsumer{reader: reader}, nil
}

// Consume reads signals until the context is canceled.
func (c *KafkaConsumer) Consume(ctx context.Context, handle func(context.Context, Signal) error) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		message, err := c.reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("read kafka message: %w", err)
		}
		if signal, decodeErr := Decode(message.Value); decodeErr == nil {
			_ = handle(ctx, signal)
		}
	}
}

// Close closes the reader.
func (c *KafkaConsumer) Close() error { return c.reader.Close() }

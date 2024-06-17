package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

type WebhookConfig struct {
	URL string `json:"url"`
	Key string `json:"key"`
}

type ConsumerConfig struct {
	DurableName string        `json:"durable_name"`
	Webhook     WebhookConfig `json:"webhook"`
}

type StreamConfig struct {
	// No subject is specified, so the default subject will be the same name as the stream.
	// See https://docs.nats.io/nats-concepts/jetstream/streams#subjects.
	Name      string           `json:"name"`
	Consumers []ConsumerConfig `json:"consumers"`
}

type SubscriberConfig struct {
	Streams []StreamConfig `json:"streams"`
	Webhook WebhookConfig  `json:"webhook"`

	HTTPClient *http.Client `json:"-"`
}

// Subscriber is a message subscriber that listens to messages from NATS JetStream
// and forwards them to the corresponding webhooks.
type Subscriber struct {
	consumers []*Consumer
	contexts  []jetstream.ConsumeContext
}

func NewSubscriber(logger *slog.Logger, js jetstream.JetStream, cfg *SubscriberConfig) (*Subscriber, error) {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Timeout: 30 * time.Second,
		}
	}

	logger = logger.With("in", "Subscriber")
	ctx := context.Background()

	var consumers []*Consumer
	for _, streamCfg := range cfg.Streams {
		stream, err := getOrCreateStream(ctx, js, streamCfg.Name)
		if err != nil {
			return nil, err
		}

		for _, consumerCfg := range streamCfg.Consumers {
			consumer, err := getOrCreateConsumer(ctx, stream, consumerCfg.DurableName)
			if err != nil {
				return nil, err
			}
			// Use the default webhook, if any, if no consumer webhook is specified.
			webhook := consumerCfg.Webhook
			if webhook.URL == "" && cfg.Webhook.URL != "" {
				webhook = cfg.Webhook
			}
			consumers = append(consumers, &Consumer{
				consumer:   consumer,
				logger:     logger,
				httpClient: cfg.HTTPClient,
				webhook:    webhook,
			})

			logger.Info("Subscribing to messages", "stream", streamCfg.Name, "consumer", consumerCfg.DurableName)
		}
	}

	if len(consumers) == 0 {
		logger.Warn("No consumers configured")
	}

	return &Subscriber{
		consumers: consumers,
	}, nil
}

func (s *Subscriber) Subscribe() error {
	for _, consumer := range s.consumers {
		cctx, err := consumer.Consume()
		if err != nil {
			return err
		}
		s.contexts = append(s.contexts, cctx)
	}
	return nil
}

func (s *Subscriber) Stop() {
	for _, cctx := range s.contexts {
		cctx.Stop()
	}
}

// Consumer is a message consumer that forwards messages to the corresponding webhooks.
type Consumer struct {
	consumer jetstream.Consumer

	logger     *slog.Logger
	httpClient *http.Client
	webhook    WebhookConfig
}

func (c *Consumer) Consume() (jetstream.ConsumeContext, error) {
	return c.consumer.Consume(c.send)
}

func (c *Consumer) send(msg jetstream.Msg) {
	m := Message{
		Subject: msg.Subject(),
		Data:    msg.Data(),
		Header:  msg.Headers(),
	}
	c.logger.Info("Received message", m.LogArgs()...)

	webhookURL, err := url.Parse(c.webhook.URL)
	if err != nil {
		c.logger.Error("invalid webhook url", "err", err)
		return
	}

	// Set subject as a query parameter.
	query := webhookURL.Query()
	query.Set("subject", m.Subject)
	webhookURL.RawQuery = query.Encode()

	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, "POST", webhookURL.String(), bytes.NewBuffer(m.Data))
	if err != nil {
		c.logger.Error("Error", "err", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	if c.webhook.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.webhook.Key)
	}
	for key, values := range m.Header {
		for _, v := range values {
			req.Header.Add(key, v)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Error("Error", "err", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error("Error", "err", err)
		return
	}

	if resp.StatusCode != http.StatusOK {
		c.logger.Error("Error response", "status", resp.StatusCode, "err", string(body))
		return
	}

	// Everything is OK, acknowledge the message.
	msg.Ack()
}

func getOrCreateStream(ctx context.Context, js jetstream.JetStream, streamName string) (jetstream.Stream, error) {
	stream, err := js.Stream(ctx, streamName)
	if err == nil {
		return stream, nil
	}

	// Stream not found, create a new one with default config.
	// Typically, the stream should be created administratively (using the `nats` tool).
	return js.CreateStream(ctx, jetstream.StreamConfig{
		Name: streamName,
	})
}

func getOrCreateConsumer(ctx context.Context, stream jetstream.Stream, consumerName string) (jetstream.Consumer, error) {
	consumer, err := stream.Consumer(ctx, consumerName)
	if err == nil {
		return consumer, nil
	}

	// Consumer not found, create a new one with default config.
	// Typically, the consumer should be created administratively (using the `nats` tool).
	return stream.CreateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       consumerName,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		MaxDeliver:    -1, // unlimited
	})
}

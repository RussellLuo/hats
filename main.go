package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type streamFlags []string

func (s *streamFlags) String() string {
	return fmt.Sprint(*s)
}

func (s *streamFlags) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type Config struct {
	serverAddr string
	serverKey  string
	natsURL    string
	sub        SubscriberConfig
}

func (cfg *Config) init() error {
	for _, streamCfgStr := range streams {
		var streamCfg StreamConfig
		if err := json.Unmarshal([]byte(streamCfgStr), &streamCfg); err != nil {
			return err
		}
		cfg.sub.Streams = append(cfg.sub.Streams, streamCfg)
	}

	// Use the server's key for authentication if it's the test webhook.
	testWebhookURL := fmt.Sprintf("http://127.0.0.1%s/webhook", cfg.serverAddr)
	if cfg.sub.Webhook.URL == testWebhookURL {
		cfg.sub.Webhook.Key = cfg.serverKey
	}

	return nil
}

var (
	cfg     Config
	streams streamFlags
)

func init() {
	flag.StringVar(&cfg.serverAddr, "server_addr", ":8080", "The listen address of the server")
	flag.StringVar(&cfg.serverKey, "server_key", "", "The auth key for the endpoints of the server")
	flag.StringVar(&cfg.natsURL, "nats_url", nats.DefaultURL, "The URL of the NATS server")
	flag.Var(&streams, "sub_stream", "The JSON config of a single stream")
	flag.StringVar(&cfg.sub.Webhook.URL, "sub_webhook_url", "http://127.0.0.1:8080/webhook", "The URL of the default webhook")
	flag.StringVar(&cfg.sub.Webhook.Key, "sub_webhook_key", "", "The key of the default webhook")
}

func main() {
	logger := slog.Default()

	flag.Parse()
	if err := cfg.init(); err != nil {
		logger.Error("", "err", err)
		return
	}

	// Connect to the NATS server.
	nc, err := nats.Connect(cfg.natsURL)
	if err != nil {
		logger.Error("", "err", err)
		return
	}
	defer nc.Close()
	slog.Info("Connected to NATS server", "url", nc.ConnectedUrl())

	// Create a JetStream management interface.
	js, err := jetstream.New(nc)
	if err != nil {
		logger.Error("Error", "err", err)
		return
	}

	// Create a subscriber and add a subscription.
	sub, err := NewSubscriber(logger, js, &cfg.sub)
	if err != nil {
		logger.Error("Error", "err", err)
		return
	}
	if err := sub.Subscribe(); err != nil {
		logger.Error("Error", "err", err)
		return
	}
	defer sub.Stop()

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(bearerAuth("example", cfg.serverKey))

	// Register the publisher.
	pub := NewPublisher(logger, js)
	r.Post("/pub", pub.Publish)

	// Register a webhook handler for testing purpose.
	webhook := NewTestWebhook(logger)
	r.Post("/webhook", webhook.Handle)

	logger.Info("Publishing server running", "addr", cfg.serverAddr)
	if err := http.ListenAndServe(cfg.serverAddr, r); err != nil {
		logger.Error("Error", "err", err)
	}
}

// bearerAuth implements a simple middleware handler for adding Bearer Authentication to a route.
func bearerAuth(realm string, token string) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				goto Next
			}

			if r.Header.Get("Authorization") != "Bearer "+token {
				w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s"`, realm))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

		Next:
			next.ServeHTTP(w, r)
		})
	}
}

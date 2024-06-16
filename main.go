package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"sigs.k8s.io/yaml"
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
	RawConfig string `json:"-"`

	ServerAddr string           `json:"server_addr"`
	ServerKey  string           `json:"server_key"`
	NatsURL    string           `json:"nats_url"`
	Sub        SubscriberConfig `json:"sub"`
}

func (cfg *Config) init() error {
	if err := cfg.parse(); err != nil {
		return err
	}

	// Use the server's key for authentication if it's the test webhook.
	testWebhookURL := fmt.Sprintf("http://127.0.0.1%s/webhook", cfg.ServerAddr)
	if cfg.Sub.Webhook.URL == testWebhookURL {
		cfg.Sub.Webhook.Key = cfg.ServerKey
	}

	return nil
}

func (cfg *Config) parse() error {
	// If config file is specified, read the configuration from it.
	if cfg.RawConfig != "" {
		return cfg.load(cfg.RawConfig)
	}

	for _, streamCfgStr := range streams {
		var streamCfg StreamConfig
		if err := json.Unmarshal([]byte(streamCfgStr), &streamCfg); err != nil {
			return err
		}
		cfg.Sub.Streams = append(cfg.Sub.Streams, streamCfg)
	}

	return nil
}

func (cfg *Config) load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	switch ext := filepath.Ext(path); ext {
	case ".yaml":
		return yaml.Unmarshal(data, cfg)
	case ".json":
		return json.Unmarshal(data, cfg)
	default:
		return fmt.Errorf("unsupported config file format: %s", ext)
	}
}

var (
	cfg     Config
	streams streamFlags
)

func init() {
	flag.StringVar(&cfg.RawConfig, "config", "", "YAML/JSON file to read configuration from")
	flag.StringVar(&cfg.ServerAddr, "server_addr", ":8080", "The listen address of the server")
	flag.StringVar(&cfg.ServerKey, "server_key", "", "The auth key for the endpoints of the server")
	flag.StringVar(&cfg.NatsURL, "nats_url", nats.DefaultURL, "The URL of the NATS server")
	flag.Var(&streams, "sub_stream", "The JSON config of a single stream")
	flag.StringVar(&cfg.Sub.Webhook.URL, "sub_webhook_url", "http://127.0.0.1:8080/webhook", "The URL of the default webhook")
	flag.StringVar(&cfg.Sub.Webhook.Key, "sub_webhook_key", "", "The key of the default webhook")
}

func main() {
	logger := slog.Default()

	flag.Parse()
	if err := cfg.init(); err != nil {
		logger.Error("", "err", err)
		return
	}

	// Connect to the NATS server.
	nc, err := nats.Connect(cfg.NatsURL)
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
	sub, err := NewSubscriber(logger, js, &cfg.Sub)
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
	r.Use(bearerAuth("example", cfg.ServerKey))

	// Register the publisher.
	pub := NewPublisher(logger, js)
	r.Post("/pub", pub.Publish)

	// Register a webhook handler for testing purpose.
	webhook := NewTestWebhook(logger)
	r.Post("/webhook", webhook.Handle)

	logger.Info("Publishing server running", "addr", cfg.ServerAddr)
	if err := http.ListenAndServe(cfg.ServerAddr, r); err != nil {
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

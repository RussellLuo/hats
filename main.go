package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

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

	Nats   string           `json:"nats"`
	Server ServerConfig     `json:"pub"`
	Sub    SubscriberConfig `json:"sub"`
}

func (cfg *Config) init() error {
	if err := cfg.parse(); err != nil {
		return err
	}

	// Use the server's key for authentication if it's the test webhook.
	testWebhookURL := fmt.Sprintf("http://127.0.0.1%s/webhook", cfg.Server.Addr)
	if cfg.Sub.Webhook.URL == testWebhookURL {
		cfg.Sub.Webhook.Key = cfg.Server.Key
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
	flag.StringVar(&cfg.Nats, "nats", nats.DefaultURL, "The URL of the NATS server")
	flag.StringVar(&cfg.Server.Addr, "pub_addr", ":8080", "The listen address of the publishing server")
	flag.StringVar(&cfg.Server.Key, "pub_key", "", "The auth key for the endpoints of the publishing server")
	flag.Var(&streams, "sub_stream", "The JSON config of a single stream (Set multiple `-sub_stream` for multiple streams)")
	flag.StringVar(&cfg.Sub.Webhook.URL, "sub_webhook_url", "http://127.0.0.1:8080/webhook", "The URL of the default webhook")
	flag.StringVar(&cfg.Sub.Webhook.Key, "sub_webhook_key", "", "The auth key of the default webhook")
}

func main() {
	logger := slog.Default()

	flag.Parse()
	if err := cfg.init(); err != nil {
		logger.Error("Error", "err", err)
		return
	}

	// Connect to the NATS server.
	nc, err := nats.Connect(cfg.Nats)
	if err != nil {
		logger.Error("Error", "err", err)
		return
	}
	defer nc.Close()
	logger.Info("Connected to NATS server", "url", nc.ConnectedUrl())

	// Create a JetStream management interface.
	js, err := jetstream.New(nc)
	if err != nil {
		logger.Error("Error", "err", err)
		return
	}

	// Create a subscriber and add subscriptions.
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

	// Create and start a publishing server.
	pub := NewPublisher(logger, js)
	s := NewServer(logger, pub, &cfg.Server)
	if err := s.Serve(); err != nil {
		logger.Error("Error", "err", err)
	}
}

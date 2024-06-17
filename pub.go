package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type Message struct {
	Subject string              `json:"subject"`
	Data    []byte              `json:"data"`
	Header  map[string][]string `json:"header"`
}

func (m Message) LogArgs() []any {
	return []any{
		"subject", m.Subject,
		"data", string(m.Data),
		"header", fmt.Sprintf("%v", m.Header),
	}
}

func (m Message) NatsMsg() *nats.Msg {
	return &nats.Msg{
		Subject: m.Subject,
		Data:    m.Data,
		Header:  m.Header,
	}
}

// Publisher is a message publisher that receives messages from HTTP and publishes them to NATS JetStream.
type Publisher struct {
	logger *slog.Logger
	js     jetstream.JetStream
}

func NewPublisher(logger *slog.Logger, js jetstream.JetStream) *Publisher {
	return &Publisher{
		logger: logger.With("in", "Publisher"),
		js:     js,
	}
}

func (p *Publisher) Publish(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	subject := query.Get("subject")
	if subject == "" {
		http.Error(w, `missing query parameter "subject"`, http.StatusBadRequest)
		return
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read body: %s", err.Error()), http.StatusBadRequest)
		return
	}

	m := Message{
		Subject: subject,
		Data:    data,
		Header:  getNatsHeader(r.Header),
	}
	p.logger.Info("Received message", m.LogArgs()...)

	ctx := context.Background()
	if _, err = p.js.PublishMsg(ctx, m.NatsMsg()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// TestWebhook is the default webhook for testing purpose.
type TestWebhook struct {
	logger *slog.Logger `json:"-"`
}

func NewTestWebhook(logger *slog.Logger) *TestWebhook {
	return &TestWebhook{
		logger: logger.With("in", "Webhook"),
	}
}

func (tw *TestWebhook) Handle(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	subject := query.Get("subject")
	if subject == "" {
		http.Error(w, `missing query parameter "subject"`, http.StatusBadRequest)
		return
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read body: %s", err.Error()), http.StatusBadRequest)
		return
	}

	m := Message{
		Subject: subject,
		Data:    data,
		Header:  getNatsHeader(r.Header),
	}
	tw.logger.Info("Received message", m.LogArgs()...)
}

func getNatsHeader(h map[string][]string) map[string][]string {
	m := make(map[string][]string)
	for key, values := range h {
		if strings.HasPrefix(key, "Nats-") {
			m[key] = values
		}
	}
	return m
}

type ServerConfig struct {
	Addr string `json:"addr"`
	Key  string `json:"key"`
}

type Server struct {
	logger *slog.Logger
	pub    *Publisher
	cfg    *ServerConfig
}

func NewServer(logger *slog.Logger, pub *Publisher, cfg *ServerConfig) *Server {
	return &Server{
		logger: logger,
		pub:    pub,
		cfg:    cfg,
	}
}

func (s *Server) Serve() error {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(bearerAuth("example", s.cfg.Key))

	// Register the publisher.
	r.Post("/pub", s.pub.Publish)

	// Register a webhook handler for testing purpose.
	webhook := NewTestWebhook(s.logger)
	r.Post("/webhook", webhook.Handle)

	s.logger.Info("Publishing server running", "addr", s.cfg.Addr)
	return http.ListenAndServe(s.cfg.Addr, r)
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

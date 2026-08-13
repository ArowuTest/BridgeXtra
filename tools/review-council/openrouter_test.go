package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenRouterReviewSuccess(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		if r.Header.Get("Authorization") == "Bearer test-key" {
			sawAuth = true
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "x-ai/grok-4.6" {
			t.Fatalf("model=%v", body["model"])
		}
		if body["stream"] != false {
			t.Fatalf("stream=%v", body["stream"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"review output"}}]}`))
	}))
	defer srv.Close()

	c := &OpenRouterClient{Endpoint: srv.URL, APIKey: "test-key", Model: "x-ai/grok-4.6", HTTP: srv.Client(), MaxTokens: 9000, Retries: 2, Sleep: func(time.Duration) {}}
	got, err := c.Review(context.Background(), "system", "user")
	if err != nil {
		t.Fatal(err)
	}
	if got != "review output" {
		t.Fatalf("got %q", got)
	}
	if !sawAuth {
		t.Fatal("authorization header missing")
	}
}

func TestOpenRouterRetries429ThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			http.Error(w, "rate", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()
	c := &OpenRouterClient{Endpoint: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client(), MaxTokens: 100, Retries: 2, Sleep: func(time.Duration) {}}
	got, err := c.Review(context.Background(), "s", "u")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" || calls.Load() != 2 {
		t.Fatalf("got=%q calls=%d", got, calls.Load())
	}
}

func TestOpenRouterDoesNotRetry401(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := &OpenRouterClient{Endpoint: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client(), MaxTokens: 100, Retries: 2, Sleep: func(time.Duration) {}}
	if _, err := c.Review(context.Background(), "s", "u"); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d want 1", calls.Load())
	}
}

func TestOpenRouterRejectsMalformedSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()
	c := &OpenRouterClient{Endpoint: srv.URL, APIKey: "k", Model: "m", HTTP: srv.Client(), MaxTokens: 100, Retries: 0, Sleep: func(time.Duration) {}}
	if _, err := c.Review(context.Background(), "s", "u"); err == nil {
		t.Fatal("expected malformed response error")
	}
}

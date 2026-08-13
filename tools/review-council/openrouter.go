package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const defaultOpenRouterEndpoint = "https://openrouter.ai/api/v1/chat/completions"
const defaultReviewModel = "x-ai/grok-4.6"

type OpenRouterClient struct {
	Endpoint  string
	APIKey    string
	Model     string
	HTTP      *http.Client
	MaxTokens int
	Retries   int
	Sleep     func(time.Duration)
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
	Stream      bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (c *OpenRouterClient) Review(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = defaultOpenRouterEndpoint
	}
	model := c.Model
	if model == "" {
		model = defaultReviewModel
	}
	maxTokens := c.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 9000
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Minute}
	}
	sleep := c.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	retries := c.Retries
	if retries < 0 {
		retries = 0
	}
	payload, err := json.Marshal(chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.1,
		MaxTokens:   maxTokens,
		Stream:      false,
	})
	if err != nil {
		return "", err
	}

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("OpenRouter request failed: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return "", fmt.Errorf("read OpenRouter response: %w", readErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
			if retryable && attempt < retries {
				sleep(time.Duration(1<<attempt) * time.Second)
				continue
			}
			msg := strings.TrimSpace(string(body))
			if len(msg) > 500 {
				msg = msg[:500]
			}
			return "", fmt.Errorf("OpenRouter HTTP %d: %s", resp.StatusCode, msg)
		}
		var decoded chatResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			return "", fmt.Errorf("decode OpenRouter response: %w", err)
		}
		if len(decoded.Choices) == 0 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
			return "", fmt.Errorf("OpenRouter response contained no assistant content")
		}
		return decoded.Choices[0].Message.Content, nil
	}
}

func runCouncil(ctx context.Context, c *OpenRouterClient, root string, roles []Role, p Provenance, b Bundle) []ReviewerResult {
	results := make([]ReviewerResult, len(roles))
	var wg sync.WaitGroup
	for i, role := range roles {
		i, role := i, role
		wg.Add(1)
		go func() {
			defer wg.Done()
			prompt, err := buildPrompt(root, role, p, b)
			if err != nil {
				results[i] = ReviewerResult{Role: role, Error: err.Error()}
				return
			}
			content, err := c.Review(ctx,
				"You are an independent adversarial software engineering reviewer. Follow the supplied BridgeXtra role, provenance gate, and finding schema exactly.",
				prompt)
			if err != nil {
				results[i] = ReviewerResult{Role: role, Error: err.Error()}
				return
			}
			results[i] = ReviewerResult{Role: role, Content: content}
		}()
	}
	wg.Wait()
	return results
}

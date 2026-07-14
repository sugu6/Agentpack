package market

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"time"
)

const (
	defaultTimeout = 10 * time.Second
	maxRetries     = 3
	retryBaseDelay = 500 * time.Millisecond
	retryMaxDelay  = 5 * time.Second
)

type HTTPClient struct {
	client *http.Client
}

func NewHTTPClient() *HTTPClient {
	return &HTTPClient{
		client: &http.Client{
			Timeout: defaultTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        16,
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     30 * time.Second,
				// 禁用 HTTP/2，避免代理环境下空闲连接收到 400
				TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
			},
		},
	}
}

func (c *HTTPClient) Do(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "AgentPack/0.1 (+https://github.com/anomalyco/agentpack)")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	return c.client.Do(req)
}

func (c *HTTPClient) Get(ctx context.Context, url string) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		// context 已取消/超时则不再重试，避免无意义的请求
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt > 0 {
			delay := retryBackoff(attempt)
			log.Printf("http retry %d/%d for %s after %v", attempt, maxRetries, url, delay)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}

		resp, err := c.Do(req)
		if err != nil {
			// context 超时/取消则不再重试
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}

		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			drainBody(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("server returned %d", resp.StatusCode)
			continue
		}

		return resp, nil
	}
	return nil, fmt.Errorf("all %d retries exhausted: %w", maxRetries+1, lastErr)
}

func retryBackoff(attempt int) time.Duration {
	delay := time.Duration(math.Pow(2, float64(attempt))) * retryBaseDelay
	if delay > retryMaxDelay {
		delay = retryMaxDelay
	}
	jitter := time.Duration(rand.Int64N(int64(delay / 2)))
	return delay + jitter
}

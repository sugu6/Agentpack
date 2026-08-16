package market

import (
	"agentpack/internal/appmeta"
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
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
	return NewHTTPClientWithTimeout(defaultTimeout)
}

func NewHTTPClientWithTimeout(timeout time.Duration) *HTTPClient {
	return &HTTPClient{
		client: &http.Client{
			Timeout: timeout,
			// 走环境代理（HTTP_PROXY/HTTPS_PROXY/NO_PROXY）：
			// 默认 Transport 自带 ProxyFromEnvironment，自定义 Transport
			// 不设置则绕过代理直连，代理网络环境下所有请求失败
			Transport: newHTTPTransport(http.ProxyFromEnvironment),
		},
	}
}

// NewHTTPClientNoProxy 创建绕过环境代理的直连 client。
// 应用主链路（jsDelivr data API/CDN）直连优先：部分代理对 jsDelivr 域名
// 返回 403（如规则未放行），直连通常可达（jsDelivr 在中国有节点）。
func NewHTTPClientNoProxy() *HTTPClient {
	return &HTTPClient{
		client: &http.Client{
			Timeout:   defaultTimeout,
			Transport: newHTTPTransport(nil),
		},
	}
}

func newHTTPTransport(proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	return &http.Transport{
		Proxy: proxy,
		// 走环境代理（HTTP_PROXY/HTTPS_PROXY/NO_PROXY）
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second,
		// 禁用 HTTP/2，避免代理环境下空闲连接收到 400
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
	}
}

// GetWithFallback 先用当前 client 请求，遇网络错误/403/429/5xx 改用
// fallback client（直连或代理）重试一次。
// 适用场景：代理对某域名返回 403 但直连可达（或反之），双路径互相兜底。
// 2xx/404 等语义明确的响应直接返回，不触发 fallback。
func (c *HTTPClient) GetWithFallback(ctx context.Context, url string, fallback *HTTPClient) (*http.Response, error) {
	resp, err := c.Get(ctx, url)
	if err == nil && resp.StatusCode < 500 && resp.StatusCode != 403 && resp.StatusCode != 429 {
		return resp, nil
	}
	if err == nil {
		drainBody(resp.Body)
		resp.Body.Close()
	}
	if ctx.Err() != nil {
		if err != nil {
			return nil, err
		}
		return nil, ctx.Err()
	}
	log.Printf("http fallback to alternate route for %s (last error: %v)", url, err)
	return fallback.Get(ctx, url)
}

func (c *HTTPClient) Do(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		// 版本号由 appmeta 在启动时注入，避免与发布版本脱钩
		req.Header.Set("User-Agent", appmeta.UserAgent("https://github.com/sugu6/AgentPack"))
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

		if resp.StatusCode == 429 || resp.StatusCode >= 500 ||
			// GitHub API 超限时返回 403 + X-RateLimit-Remaining: 0（而非 429），
			// 携带该响应头时按限流处理重试；普通 403（权限/资源不存在）不重试。
			(resp.StatusCode == 403 && resp.Header.Get("X-RateLimit-Remaining") == "0") {
			drainBody(resp.Body)
			resp.Body.Close()
			// 限流（429 / GitHub API 配额耗尽 403+RLR:0）：未认证 API 的 reset
			// 往往数十分钟，指数退避重试必然再次失败且白白消耗配额。剩余等待
			// 超出合理预算（10s）时立即失败，不再重试——调用方走 fallback
			// 路径（jsDelivr 主链路）或跳过该仓库，而不是在限流上干耗。
			if resp.StatusCode != 500 {
				if wait := rateLimitWait(resp); wait > 10*time.Second {
					lastErr = fmt.Errorf("rate limited (reset in %s)", wait.Round(time.Second))
					break
				}
			}
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

// rateLimitWait 解析限流响应头中的剩余等待时间。
// 优先 X-RateLimit-Reset（Unix 秒时间戳，GitHub API 标准），其次 Retry-After。
// 无法解析时返回 0（调用方按正常重试处理）。
func rateLimitWait(resp *http.Response) time.Duration {
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		if ts, err := strconv.ParseInt(v, 10, 64); err == nil && ts > 0 {
			if rem := time.Until(time.Unix(ts, 0)); rem > 0 {
				return rem
			}
		}
	}
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

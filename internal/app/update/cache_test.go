package update

import (
	"testing"
	"time"
)

// cachedService 构造一个已写入 CheckUpdate 缓存的 Service，便于直接测 updateCheckCached。
func cachedService(result *UpdateCheckResult, msgKey string, msgArgs map[string]interface{}, rateLtd bool, at time.Time) *Service {
	s := NewService(nil, func() string { return "en" }, nil, nil, nil)
	s.checkAt = at
	s.checkRateLtd = rateLtd
	s.checkResult = result
	s.checkMsgKey = msgKey
	s.checkMsgArgs = msgArgs
	return s
}

func TestUpdateCheckCache_WithinTTLReturnsCached(t *testing.T) {
	s := cachedService(&UpdateCheckResult{
		HasUpdate:      false,
		CurrentVersion: "1.0.0",
		LatestVersion:  "1.0.0",
		Message:        "stale-msg",
	}, "update.message.latest", map[string]interface{}{"version": "1.0.0"}, false, time.Now())

	res := s.updateCheckCached("zh-CN")
	if res == nil || res.result == nil {
		t.Fatal("expected cached result within TTL")
	}
	// 缓存命中必须按当前语言重生成消息，不能返回缓存时的旧文案
	if res.result.Message == "stale-msg" {
		t.Errorf("message should be re-localized on cache hit, got %q", res.result.Message)
	}
	if res.result.CurrentVersion != "1.0.0" {
		t.Errorf("unexpected currentVersion: %s", res.result.CurrentVersion)
	}
}

func TestUpdateCheckCache_ExpiredReturnsNil(t *testing.T) {
	s := cachedService(&UpdateCheckResult{HasUpdate: false}, "update.message.latest", nil, false, time.Now().Add(-updateCheckTTL).Add(-time.Second))
	if res := s.updateCheckCached("en"); res != nil {
		t.Fatalf("expected nil (cache expired), got %+v", res.result)
	}
}

func TestUpdateCheckCache_RateLimitUsesLongerBackoff(t *testing.T) {
	// 已超过普通 TTL 但仍在限流退避窗口内：限流结果应继续命中缓存，
	// 避免在限流窗口内再次请求 GitHub API。
	at := time.Now().Add(-(updateCheckTTL + time.Minute))
	s := cachedService(&UpdateCheckResult{HasUpdate: false}, "update.message.rateLimited", nil, true, at)
	if res := s.updateCheckCached("en"); res == nil {
		t.Fatal("expected rate-limited result to remain cached within backoff window")
	}
}

func TestUpdateCheckCache_NoCacheReturnsNil(t *testing.T) {
	s := NewService(nil, func() string { return "en" }, nil, nil, nil)
	if res := s.updateCheckCached("en"); res != nil {
		t.Fatalf("expected nil when no cache, got %+v", res.result)
	}
}

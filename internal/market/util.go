package market

import (
	"io"
	"strconv"
	"strings"
)

// drainBody 读取并丢弃 HTTP 响应体，使连接可被复用
func drainBody(body io.Reader) {
	_, _ = io.Copy(io.Discard, body)
}

// normalizePaging 规范化分页参数：PageSize 默认 30、上限 100；Page 默认 1
// 上限 100 是 registry API (/v0.1/servers) 的 limit 参数上限,超过会返回 422
func normalizePaging(opts *SearchOptions) {
	if opts.PageSize <= 0 {
		opts.PageSize = 30
	}
	if opts.PageSize > 100 {
		opts.PageSize = 100
	}
	if opts.Page < 1 {
		opts.Page = 1
	}
}

// readErrorSnippet 读取错误响应体的前 256 字节作为调试信息,然后丢弃剩余内容
// 用于在 4xx/5xx 错误时返回有意义的错误消息,便于排查 API 调用问题
func readErrorSnippet(body io.Reader) string {
	if body == nil {
		return ""
	}
	// 限制读取 256 字节,防止超大错误响应体占用内存
	lr := io.LimitReader(body, 256)
	buf, err := io.ReadAll(lr)
	// 丢弃剩余内容,保证连接可复用
	drainBody(body)
	if err != nil || len(buf) == 0 {
		return ""
	}
	s := strings.TrimSpace(string(buf))
	// 截断到一行,避免错误消息过长且包含换行
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// isStatusErr 判断错误是否由特定 HTTP 状态码产生
// 用于 422 自动降级重试等场景
func isStatusErr(err error, code int) bool {
	if err == nil {
		return false
	}
	// 错误格式: "registry search: status 422 (...)"
	// 仅匹配 "status <code>" 子串,避免格式变动导致漏判
	prefix := "status " + strconv.Itoa(code)
	return strings.Contains(err.Error(), prefix+" ") || strings.HasSuffix(err.Error(), prefix)
}

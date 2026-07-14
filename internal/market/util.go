package market

import (
	"io"
)

// drainBody 读取并丢弃 HTTP 响应体，使连接可被复用
func drainBody(body io.Reader) {
	_, _ = io.Copy(io.Discard, body)
}

// normalizePaging 规范化分页参数：PageSize 默认 30、上限 100；Page 默认 1
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

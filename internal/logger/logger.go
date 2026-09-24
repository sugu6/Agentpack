package logger

import (
	"io"
	"log/slog"
	"os"
	"sync/atomic"
)

var (
	defaultLogger atomic.Pointer[slog.Logger]
	levelVar      slog.LevelVar
)

func init() {
	defaultLogger.Store(slog.New(newHandler(os.Stderr)))
	slog.SetDefault(defaultLogger.Load())
}

type handlerOption func(*handlerOptions)

type handlerOptions struct {
	includeCalls bool
}

func newHandler(w io.Writer, opts ...handlerOption) slog.Handler {
	hOpts := &handlerOptions{
		includeCalls: true,
	}
	for _, opt := range opts {
		opt(hOpts)
	}

	return slog.NewTextHandler(w, &slog.HandlerOptions{
		Level:     &levelVar,
		AddSource: hOpts.includeCalls,
	})
}

func Debug(msg string, args ...any) {
	defaultLogger.Load().Debug(msg, args...)
}

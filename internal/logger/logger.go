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

func WithIncludeCalls(include bool) handlerOption {
	return func(o *handlerOptions) {
		o.includeCalls = include
	}
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

func Info(msg string, args ...any) {
	defaultLogger.Load().Info(msg, args...)
}

func Error(msg string, args ...any) {
	defaultLogger.Load().Error(msg, args...)
}

func Warn(msg string, args ...any) {
	defaultLogger.Load().Warn(msg, args...)
}

func Debug(msg string, args ...any) {
	defaultLogger.Load().Debug(msg, args...)
}

func SetLevel(l slog.Level) {
	levelVar.Set(l)
}

func SetOutput(w io.Writer) {
	h := newHandler(w)
	newLogger := slog.New(h)
	defaultLogger.Store(newLogger)
	slog.SetDefault(newLogger)
}

func WithGroup(name string) *slog.Logger {
	return defaultLogger.Load().WithGroup(name)
}

func With(args ...any) *slog.Logger {
	return defaultLogger.Load().With(args...)
}

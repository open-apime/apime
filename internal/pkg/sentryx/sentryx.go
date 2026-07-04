// Package sentryx is a thin wrapper over github.com/getsentry/sentry-go that
// centralizes initialization, silent no-op when SENTRY_DSN is not set, capture
// of whatsmeow errors (via zap hook) and the Gin middleware.
//
// Without a configured DSN, all functions become no-ops — there is no overhead
// and no network call is made.
package sentryx

import (
	"context"
	"time"

	"github.com/getsentry/sentry-go"
)

// Config is the subset we consume from internal/config.SentryConfig — duplicated
// as a plain struct to avoid an import cycle.
type Config struct {
	DSN              string
	Environment      string
	Release          string
	SampleRate       float64
	TracesSampleRate float64
	ServerName       string
}

var enabled bool

// IsEnabled reports whether Sentry was initialized with a valid DSN.
func IsEnabled() bool { return enabled }

// Init initializes Sentry when a DSN is configured. Without a DSN it is a silent
// no-op. Init errors are only reported by the caller (log) — they do not bring
// down the process.
func Init(cfg Config) error {
	if cfg.DSN == "" {
		enabled = false
		return nil
	}
	opts := sentry.ClientOptions{
		Dsn:              cfg.DSN,
		Environment:      cfg.Environment,
		Release:          cfg.Release,
		ServerName:       cfg.ServerName,
		SampleRate:       cfg.SampleRate,
		TracesSampleRate: cfg.TracesSampleRate,
		AttachStacktrace: true,
		EnableTracing:    cfg.TracesSampleRate > 0,
	}
	if opts.SampleRate == 0 {
		opts.SampleRate = 1.0
	}
	if err := sentry.Init(opts); err != nil {
		enabled = false
		return err
	}
	enabled = true
	return nil
}

// Flush blocks until pending events are sent or the timeout elapses.
func Flush(timeout time.Duration) {
	if !enabled {
		return
	}
	sentry.Flush(timeout)
}

// CaptureError sends an error with optional tags.
func CaptureError(err error, tags map[string]string) {
	if !enabled || err == nil {
		return
	}
	hub := sentry.CurrentHub().Clone()
	hub.WithScope(func(scope *sentry.Scope) {
		for k, v := range tags {
			scope.SetTag(k, v)
		}
		hub.CaptureException(err)
	})
}

// CaptureMessage sends a message with a level.
func CaptureMessage(msg string, level sentry.Level, tags map[string]string) {
	if !enabled || msg == "" {
		return
	}
	hub := sentry.CurrentHub().Clone()
	hub.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(level)
		for k, v := range tags {
			scope.SetTag(k, v)
		}
		hub.CaptureMessage(msg)
	})
}

// Recover must be called via defer in critical goroutines to report panics to
// Sentry and re-throw them to preserve the original behavior.
func Recover() {
	if r := recover(); r != nil {
		if enabled {
			sentry.CurrentHub().Recover(r)
			sentry.Flush(2 * time.Second)
		}
		panic(r)
	}
}

// RecoverWithContext is like Recover, with scope derived from ctx.
func RecoverWithContext(ctx context.Context) {
	if r := recover(); r != nil {
		if enabled {
			sentry.GetHubFromContext(ctx).Recover(r)
			sentry.Flush(2 * time.Second)
		}
		panic(r)
	}
}

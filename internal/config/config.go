package config

import (
	"fmt"
	"log"

	"github.com/caarlos0/env/v10"
)

type Config struct {
	App         AppConfig
	DB          DatabaseConfig
	Redis       RedisConfig
	JWT         JWTConfig
	Log         LogConfig
	Storage     StorageConfig
	RateLimit   RateLimitConfig
	IPRateLimit IPRateLimitConfig
	WhatsApp    WhatsAppConfig
	Webhook     WebhookConfig
	Dashboard   DashboardConfig
	Sentry      SentryConfig
}

type StorageConfig struct {
	Driver          string `env:"DB_DRIVER" envDefault:"sqlite"`
	DataDir         string `env:"DATA_DIR" envDefault:"/app/data"`
	MediaTTLSeconds int    `env:"MEDIA_TTL_SECONDS" envDefault:"7200"`
}

type AppConfig struct {
	Env           string `env:"APP_ENV" envDefault:"development"`
	Port          string `env:"PORT" envDefault:"8080"`
	BaseURL       string `env:"APP_BASE_URL" envDefault:"http://localhost:8080"`
	OutboxWorkers int    `env:"OUTBOX_WORKERS" envDefault:"5"`
}

type DatabaseConfig struct {
	URL      string `env:"DATABASE_URL"`
	Host     string `env:"DB_HOST" envDefault:"localhost"`
	Port     int    `env:"DB_PORT" envDefault:"5432"`
	User     string `env:"DB_USER" envDefault:"postgres"`
	Password string `env:"DB_PASSWORD" envDefault:"postgres"`
	Name     string `env:"DB_NAME" envDefault:"postgres"`
	SSLMode  string `env:"DB_SSLMODE" envDefault:"disable"`
}

// DSN returns the connection string in a format accepted by pgxpool.
func (cfg DatabaseConfig) DSN() string {
	if cfg.URL != "" {
		return cfg.URL
	}
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Name, cfg.SSLMode,
	)
}

type RedisConfig struct {
	Addr     string `env:"REDIS_ADDR" envDefault:"localhost:6379"`
	Password string `env:"REDIS_PASSWORD" envDefault:""`
	DB       int    `env:"REDIS_DB" envDefault:"0"`
	Enabled  bool   `env:"REDIS_ENABLED" envDefault:"false"`
}

type RateLimitConfig struct {
	Enabled       bool   `env:"RATE_LIMIT_ENABLED" envDefault:"true"`
	Requests      int    `env:"RATE_LIMIT_REQUESTS" envDefault:"300"`
	WindowSeconds int    `env:"RATE_LIMIT_WINDOW_SECONDS" envDefault:"60"`
	Prefix        string `env:"RATE_LIMIT_PREFIX" envDefault:"ratelimit:api"`
}

type IPRateLimitConfig struct {
	Enabled        bool `env:"IP_RATE_LIMIT_ENABLED" envDefault:"true"`
	Requests       int  `env:"IP_RATE_LIMIT_REQUESTS" envDefault:"100"`
	WindowSeconds  int  `env:"IP_RATE_LIMIT_WINDOW_SECONDS" envDefault:"900"`
	SkipPrivateIPs bool `env:"IP_RATE_LIMIT_SKIP_PRIVATE_IPS" envDefault:"true"`
}

type JWTConfig struct {
	Secret   string `env:"JWT_SECRET,required"`
	ExpHours int    `env:"JWT_EXP_HOURS" envDefault:"24"`
}

type LogConfig struct {
	Level string `env:"LOG_LEVEL" envDefault:"debug"`
}

type WhatsAppConfig struct {
	SessionKeyEnc             string `env:"WHATSAPP_SESSION_KEY_ENC" envDefault:"apime-session-key-change-in-production"`
	JIDCachePositiveTTLHours  int    `env:"WHATSAPP_JID_CACHE_POSITIVE_TTL_HOURS" envDefault:"24"`
	JIDCacheNegativeTTLDays   int    `env:"WHATSAPP_JID_CACHE_NEGATIVE_TTL_DAYS" envDefault:"1"`
	JIDCachePositiveDBTTLDays int    `env:"WHATSAPP_JID_CACHE_POSITIVE_DB_TTL_DAYS" envDefault:"15"`
}

type WebhookConfig struct {
	Workers int `env:"WEBHOOK_WORKERS" envDefault:"4"`
}

type DashboardConfig struct {
	Enabled  bool   `env:"DASHBOARD_ENABLED" envDefault:"true"`
	Timezone string `env:"DASHBOARD_TIMEZONE" envDefault:""`
}

// SentryConfig — without a DSN, the integration stays disabled (no-op).
type SentryConfig struct {
	DSN              string  `env:"SENTRY_DSN" envDefault:""`
	Environment      string  `env:"SENTRY_ENVIRONMENT" envDefault:""`
	Release          string  `env:"SENTRY_RELEASE" envDefault:""`
	ServerName       string  `env:"SENTRY_SERVER_NAME" envDefault:""`
	SampleRate       float64 `env:"SENTRY_SAMPLE_RATE" envDefault:"1.0"`
	TracesSampleRate float64 `env:"SENTRY_TRACES_SAMPLE_RATE" envDefault:"0.0"`
}

// Load loads the application configuration.
func Load() Config {
	cfg := Config{}
	if err := env.Parse(&cfg); err != nil {
		log.Fatalf("config: could not load variables: %v", err)
	}
	return cfg
}

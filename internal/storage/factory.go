package storage

import (
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/config"
	"github.com/open-apime/apime/internal/pkg/queue"
	queue_memory "github.com/open-apime/apime/internal/pkg/queue/memory"
	queue_redis "github.com/open-apime/apime/internal/pkg/queue/redis"
	"github.com/open-apime/apime/internal/pkg/ratelimiter"
	limiter_memory "github.com/open-apime/apime/internal/pkg/ratelimiter/memory"
	limiter_redis "github.com/open-apime/apime/internal/pkg/ratelimiter/redis"
	"github.com/open-apime/apime/internal/storage/postgres"
	storage_redis "github.com/open-apime/apime/internal/storage/redis"
	"github.com/open-apime/apime/internal/storage/sqlite"
)

type Repositories struct {
	Instance     InstanceRepository
	Message      MessageRepository
	EventLog     EventLogRepository
	User         UserRepository
	APIToken     APITokenRepository
	DeviceConfig DeviceConfigRepository
	RedisClient  *storage_redis.Client // Pode ser nil se Redis estiver desabilitado
	WebhookQueue queue.Queue
	RateLimiter  ratelimiter.Limiter
}

func NewRepositories(cfg config.Config, log *zap.Logger) (*Repositories, error) {
	log.Info("inicializando repositórios",
		zap.String("driver", cfg.Storage.Driver),
	)

	var (
		webhookQueue queue.Queue
		rateLimiter  ratelimiter.Limiter
		storeRedis   *storage_redis.Client
		err          error
	)

	// Inicializa Redis apenas se explicitamente habilitado
	useRedis := cfg.Redis.Enabled

	if useRedis {
		log.Info("inicializando Redis...")
		storeRedis, err = storage_redis.New(cfg.Redis, log)
		if err != nil {
			log.Error("erro ao conectar com Redis", zap.Error(err))
			return nil, err
		}

		redisClient := storeRedis.RDB()
		webhookQueue = queue_redis.NewQueue(redisClient, "webhook:events")
		rateLimiter = limiter_redis.NewLimiter(redisClient)
		log.Info("Redis conectado, fila e limiter configurados")
	} else {
		log.Info("usando implementações em memória (Redis desabilitado)")
		webhookQueue = queue_memory.NewQueue(10000)
		rateLimiter = limiter_memory.NewLimiter()
		storeRedis = nil
	}

	switch cfg.Storage.Driver {
	case "sqlite", "":
		log.Debug("criando conexão com SQLite")
		db, err := sqlite.New(cfg.Storage.DataDir, log)
		if err != nil {
			log.Error("erro ao conectar com SQLite", zap.Error(err))
			return nil, err
		}

		log.Info("repositórios SQLite criados com sucesso", zap.String("data_dir", cfg.Storage.DataDir))
		return &Repositories{
			Instance:     sqlite.NewInstanceRepository(db),
			Message:      sqlite.NewMessageRepository(db),
			EventLog:     sqlite.NewEventLogRepository(db),
			User:         sqlite.NewUserRepository(db),
			APIToken:     sqlite.NewAPITokenRepository(db),
			DeviceConfig: sqlite.NewDeviceConfigRepository(db),
			RedisClient:  storeRedis,
			WebhookQueue: webhookQueue,
			RateLimiter:  rateLimiter,
		}, nil

	case "postgres":
		log.Debug("criando conexão com PostgreSQL")
		db, err := postgres.New(cfg.DB, log)
		if err != nil {
			log.Error("erro ao conectar com PostgreSQL", zap.Error(err))
			return nil, err
		}

		log.Info("repositórios PostgreSQL criados com sucesso")
		return &Repositories{
			Instance:     postgres.NewInstanceRepository(db),
			Message:      postgres.NewMessageRepository(db),
			EventLog:     postgres.NewEventLogRepository(db),
			User:         postgres.NewUserRepository(db),
			APIToken:     postgres.NewAPITokenRepository(db),
			DeviceConfig: postgres.NewDeviceConfigRepository(db),
			RedisClient:  storeRedis,
			WebhookQueue: webhookQueue,
			RateLimiter:  rateLimiter,
		}, nil

	default:
		log.Error("driver de storage desconhecido",
			zap.String("driver", cfg.Storage.Driver),
		)
		return nil, &ErrUnknownDriver{Driver: cfg.Storage.Driver}
	}
}

type ErrUnknownDriver struct {
	Driver string
}

func (e *ErrUnknownDriver) Error() string {
	return "storage: driver desconhecido: " + e.Driver
}

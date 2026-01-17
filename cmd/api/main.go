package main

import (
	"context"
	"log"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/api/handler"
	"github.com/open-apime/apime/internal/api/middleware"
	"github.com/open-apime/apime/internal/app"
	"github.com/open-apime/apime/internal/config"
	"github.com/open-apime/apime/internal/dashboard"
	"github.com/open-apime/apime/internal/logger"
	"github.com/open-apime/apime/internal/server"
	"github.com/open-apime/apime/internal/service/api_token"
	"github.com/open-apime/apime/internal/service/auth"
	device_config "github.com/open-apime/apime/internal/service/device_config"
	"github.com/open-apime/apime/internal/service/instance"
	"github.com/open-apime/apime/internal/service/message"
	"github.com/open-apime/apime/internal/service/user"
	"github.com/open-apime/apime/internal/session/whatsmeow"
	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/storage/media"
	"github.com/open-apime/apime/internal/storage/model"
	"github.com/open-apime/apime/internal/webhook"
	"github.com/open-apime/apime/internal/webhook/delivery"
)

type instanceCheckerAdapter struct {
	repo storage.InstanceRepository
}

func (a *instanceCheckerAdapter) HasWebhook(ctx context.Context, instanceID string) bool {
	inst, err := a.repo.GetByID(ctx, instanceID)
	if err != nil {
		return false
	}
	return inst.WebhookURL != ""
}

func main() {
	cfg := config.Load()

	logr, err := logger.New(cfg.App.Env, cfg.Log.Level)
	if err != nil {
		log.Fatalf("logger: %v", err)
	}
	defer logr.Sync()

	sessionDir := filepath.Join(cfg.Storage.DataDir, "sessions")
	mediaDir := filepath.Join(cfg.Storage.DataDir, "media")

	logr.Info("iniciando aplicação",
		zap.String("env", cfg.App.Env),
		zap.String("log_level", cfg.Log.Level),
		zap.String("port", cfg.App.Port),
		zap.String("db_driver", cfg.Storage.Driver),
		zap.String("data_dir", cfg.Storage.DataDir),
	)

	repos, err := storage.NewRepositories(cfg, logr)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}

	// Build PostgreSQL connection string for WhatsMeow sessions (empty if using SQLite)
	pgConnString := ""
	if cfg.Storage.Driver == "postgres" {
		pgConnString = cfg.DB.DSN()
	}

	sessionManager := whatsmeow.NewManager(logr, cfg.WhatsApp.SessionKeyEnc, cfg.Storage.Driver, sessionDir, pgConnString, repos.DeviceConfig, repos.Instance, repos.HistorySync)

	instanceService := instance.NewServiceWithSessionMessagesAndEventLogs(repos.Instance, repos.Message, repos.EventLog, sessionManager)

	sessionManager.SetStatusChangeCallback(func(instanceID string, status string) {
		ctx := context.Background()
		var instanceStatus model.InstanceStatus
		switch status {
		case "active":
			instanceStatus = model.InstanceStatusActive
		case "error":
			instanceStatus = model.InstanceStatusError
		default:
			instanceStatus = model.InstanceStatusPending
		}
		if _, err := instanceService.UpdateStatus(ctx, instanceID, instanceStatus); err != nil {
			logr.Warn("erro ao atualizar status da instância", zap.String("instance_id", instanceID), zap.Error(err))
		}
	})

	mediaTTL := time.Duration(cfg.Storage.MediaTTLSeconds) * time.Second
	mediaStorage, err := media.NewStorage(mediaDir, mediaTTL, logr)
	if err != nil {
		log.Fatalf("media storage: %v", err)
	}
	logr.Info("media storage inicializado", zap.String("dir", mediaDir), zap.Duration("ttl", mediaTTL))

	mediaHandler := handler.NewMediaHandler(mediaStorage)

	logr.Info("inicializando sistema de webhooks")
	instanceWebhookChecker := &instanceCheckerAdapter{repo: repos.Instance}
	eventHandler := webhook.NewEventHandler(repos.WebhookQueue, logr, mediaStorage, cfg.App.BaseURL, instanceWebhookChecker)
	sessionManager.SetEventHandler(eventHandler)
	logr.Info("event handler configurado")

	webhookDelivery := delivery.NewDelivery(logr, 3)
	webhookPool := webhook.NewPool(repos.WebhookQueue, repos.Instance, webhookDelivery, logr, cfg.Webhook.Workers)
	go webhookPool.Start(context.Background())
	logr.Info("webhook pool iniciada", zap.Int("workers", cfg.Webhook.Workers))

	logr.Info("restaurando sessões...")
	instances, err := instanceService.List(context.Background())
	if err == nil {
		var allInstanceIDs []string
		for _, inst := range instances {
			allInstanceIDs = append(allInstanceIDs, inst.ID)
		}
		if len(allInstanceIDs) > 0 {
			logr.Info("tentando restaurar sessões",
				zap.Int("total", len(allInstanceIDs)),
			)
			sessionManager.RestoreAllSessions(context.Background(), allInstanceIDs)
			time.Sleep(3 * time.Second)
		} else {
			logr.Info("nenhuma instância encontrada para restaurar")
		}
	} else {
		logr.Warn("erro ao listar instâncias para restauração", zap.Error(err))
	}

	logr.Debug("inicializando serviços")
	messageService := message.NewServiceWithSession(repos.Message, sessionManager, repos.Instance)
	apiTokenService := api_token.NewService(repos.APIToken)
	userService := user.NewService(repos.User, apiTokenService, instanceService)
	authService := auth.NewService(cfg.JWT.Secret, cfg.JWT.ExpHours, repos.User)
	deviceConfigService := device_config.NewService(repos.DeviceConfig)
	logr.Debug("serviços inicializados")

	instanceHandler := handler.NewInstanceHandlerWithSession(instanceService, logr, sessionManager)
	messageHandler := handler.NewMessageHandler(messageService)
	whatsAppHandler := handler.NewWhatsAppHandler(sessionManager)
	authHandler := handler.NewAuthHandler(authService)
	apiTokenHandler := handler.NewAPITokenHandler(apiTokenService)
	userHandler := handler.NewUserHandler(userService)
	healthHandler := handler.NewHealthHandler()

	rateLimitOpts := middleware.RateLimitOption{
		Enabled:  cfg.RateLimit.Enabled,
		Requests: cfg.RateLimit.Requests,
		Window:   time.Duration(cfg.RateLimit.WindowSeconds) * time.Second,
		Prefix:   cfg.RateLimit.Prefix,
		Logger:   logr,
		Limiter:  repos.RateLimiter,
	}

	router := server.NewRouter(server.Options{
		Env:             cfg.App.Env,
		AuthSecret:      cfg.JWT.Secret,
		HTMLTemplate:    dashboard.HTMLTemplate(),
		InstanceHandler: instanceHandler,
		MessageHandler:  messageHandler,
		WhatsAppHandler: whatsAppHandler,
		AuthHandler:     authHandler,
		APITokenHandler: apiTokenHandler,
		APITokenService: apiTokenService,
		InstanceRepo:    repos.Instance,
		HealthHandler:   healthHandler,
		UserHandler:     userHandler,
		MediaHandler:    mediaHandler,
		WebhookPool:     webhookPool,
		RateLimit:       rateLimitOpts,
	})

	if cfg.Dashboard.Enabled {
		dashboard.Register(router, dashboard.Options{
			AuthService:         authService,
			InstanceService:     instanceService,
			UserService:         userService,
			APITokenService:     apiTokenService,
			DeviceConfigService: deviceConfigService,
			SessionManager:      sessionManager,
			JWTSecret:           cfg.JWT.Secret,
			DocsDirectory:       ".",
			BaseURL:             cfg.App.BaseURL,
			Logger:              logr,
			EnableDashboard:     true,
		})
	} else {
		logr.Info("dashboard desativado via configuração")
	}

	logr.Debug("criando aplicação")
	application := app.New(cfg, logr, router)
	logr.Info("aplicação criada, iniciando servidor")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := application.Run(context.Background()); err != nil {
			logr.Error("servidor finalizado com erro", zap.Error(err))
			errCh <- err
		}
	}()

	logr.Debug("aguardando sinais de encerramento ou erros do servidor")
	select {
	case <-ctx.Done():
		logr.Info("sinal de encerramento recebido",
			zap.String("signal", "SIGINT/SIGTERM"),
		)
	case err := <-errCh:
		if err != nil {
			logr.Error("servidor finalizado com erro", zap.Error(err))
		} else {
			logr.Info("servidor finalizado normalmente")
		}
	}

	logr.Info("iniciando shutdown graceful")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	webhookPool.Stop()
	logr.Info("webhook pool encerrada")

	if repos.RedisClient != nil {
		if err := repos.RedisClient.Close(); err != nil {
			logr.Warn("erro ao fechar conexão Redis", zap.Error(err))
		} else {
			logr.Info("conexão Redis fechada")
		}
	}

	if err := application.Shutdown(shutdownCtx); err != nil {
		logr.Error("erro ao encerrar servidor", zap.Error(err))
	} else {
		logr.Info("servidor encerrado com sucesso")
	}
}

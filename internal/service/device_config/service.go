package device_config

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/storage/model"
)

var (
	ErrInvalidPlatformType = errors.New("tipo de plataforma inválido")
)

var validPlatformTypes = map[string]bool{
	"DESKTOP":       true,
	"CHROME":        true,
	"FIREFOX":       true,
	"SAFARI":        true,
	"EDGE":          true,
	"IPAD":          true,
	"ANDROID_PHONE": true,
	"IOS_PHONE":     true,
}

type Service struct {
	repo storage.DeviceConfigRepository
}

func NewService(repo storage.DeviceConfigRepository) *Service {
	return &Service{repo: repo}
}

type UpdateInput struct {
	PlatformType string
	OSName       string
}

func (s *Service) Get(ctx context.Context) (model.DeviceConfig, error) {
	return s.repo.Get(ctx)
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (model.DeviceConfig, error) {
	if input.PlatformType != "" && !validPlatformTypes[strings.ToUpper(input.PlatformType)] {
		return model.DeviceConfig{}, ErrInvalidPlatformType
	}

	current, err := s.repo.Get(ctx)
	if err != nil {
		current = model.DeviceConfig{
			ID:           "00000000-0000-0000-0000-000000000001",
			PlatformType: "DESKTOP",
			OSName:       "ApiMe",
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
	}

	if input.PlatformType != "" {
		current.PlatformType = strings.ToUpper(input.PlatformType)
	}
	if input.OSName != "" {
		current.OSName = strings.TrimSpace(input.OSName)
	}

	if current.OSName == "" {
		current.OSName = "ApiMe"
	}
	if current.PlatformType == "" {
		current.PlatformType = "DESKTOP"
	}

	return s.repo.Update(ctx, current)
}

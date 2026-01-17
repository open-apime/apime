package session

import (
	"context"

	"github.com/open-apime/apime/internal/storage/model"
)

type Manager interface {
	Start(ctx context.Context, instance model.Instance) error
	Stop(ctx context.Context, instanceID string) error
	Status(ctx context.Context, instanceID string) (model.InstanceStatus, error)
}

type StubManager struct{}

func NewStubManager() *StubManager {
	return &StubManager{}
}

func (m *StubManager) Start(ctx context.Context, instance model.Instance) error { return nil }
func (m *StubManager) Stop(ctx context.Context, instanceID string) error        { return nil }
func (m *StubManager) Status(ctx context.Context, instanceID string) (model.InstanceStatus, error) {
	return model.InstanceStatusPending, nil
}

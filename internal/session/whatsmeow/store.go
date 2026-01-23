package whatsmeow

import (
	"sync"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// InMemMessageStore implementa a interface MessageStore do whatsmeow.
// Ele armazena mensagens em memória para permitir que retries (reenvios)
// funcionem corretamente mesmo quando vêm de dispositivos secundários ou LIDs.
type InMemMessageStore struct {
	messages map[string]*waE2E.Message
	mu       sync.RWMutex
	maxSize  int
	order    []string
}

func NewInMemMessageStore(maxSize int) *InMemMessageStore {
	if maxSize <= 0 {
		maxSize = 1000
	}
	return &InMemMessageStore{
		messages: make(map[string]*waE2E.Message),
		maxSize:  maxSize,
		order:    make([]string, 0, maxSize),
	}
}

func (s *InMemMessageStore) PutMessage(chat types.JID, id string, msg *waE2E.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Se a mensagem já existe, apenas atualizamos
	if _, ok := s.messages[id]; ok {
		s.messages[id] = msg
		return nil
	}

	// Se atingiu o limite, removemos o mais antigo (FIFO)
	if len(s.order) >= s.maxSize {
		oldest := s.order[0]
		delete(s.messages, oldest)
		s.order = s.order[1:]
	}

	s.messages[id] = msg
	s.order = append(s.order, id)
	return nil
}

func (s *InMemMessageStore) GetMessage(chat types.JID, id string) (*waE2E.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	msg, ok := s.messages[id]
	if !ok {
		return nil, nil
	}
	return msg, nil
}

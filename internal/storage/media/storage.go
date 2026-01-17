package media

import (
	"context"
	"crypto/md5"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
)

type Storage struct {
	baseDir string
	ttl     time.Duration
	log     *zap.Logger
	mu      sync.RWMutex
}

func NewStorage(baseDir string, ttl time.Duration, log *zap.Logger) (*Storage, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("criar diretório de mídia: %w", err)
	}

	s := &Storage{
		baseDir: baseDir,
		ttl:     ttl,
		log:     log,
	}

	go s.startCleanupJob()

	return s, nil
}

func (s *Storage) Save(ctx context.Context, instanceID string, messageID string, data []byte, mimetype string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	instanceDir := filepath.Join(s.baseDir, instanceID)
	if err := os.MkdirAll(instanceDir, 0755); err != nil {
		return "", fmt.Errorf("criar diretório da instância: %w", err)
	}

	hash := md5.Sum(data)
	ext := getExtensionFromMimetype(mimetype)
	mediaID := fmt.Sprintf("%s_%x%s", messageID, hash[:4], ext)

	filePath := filepath.Join(instanceDir, mediaID)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return "", fmt.Errorf("salvar arquivo: %w", err)
	}

	s.log.Info("mídia salva",
		zap.String("instance_id", instanceID),
		zap.String("media_id", mediaID),
		zap.Int("size", len(data)),
		zap.String("mimetype", mimetype),
	)

	return mediaID, nil
}

func (s *Storage) Get(ctx context.Context, instanceID string, mediaID string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filePath := filepath.Join(s.baseDir, instanceID, mediaID)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("mídia não encontrada")
		}
		return nil, fmt.Errorf("ler arquivo: %w", err)
	}

	return data, nil
}

func (s *Storage) Exists(instanceID string, mediaID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filePath := filepath.Join(s.baseDir, instanceID, mediaID)
	_, err := os.Stat(filePath)
	return err == nil
}

// GetPath retorna o caminho completo do arquivo
func (s *Storage) GetPath(instanceID string, mediaID string) string {
	return filepath.Join(s.baseDir, instanceID, mediaID)
}

// startCleanupJob inicia job de limpeza periódica
func (s *Storage) startCleanupJob() {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		s.cleanup()
	}
}

// cleanup remove arquivos mais antigos que o TTL
func (s *Storage) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.log.Info("iniciando limpeza de mídia temporária", zap.Duration("ttl", s.ttl))

	var deleted, checked int
	cutoff := time.Now().Add(-s.ttl)

	err := filepath.WalkDir(s.baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Ignorar erros e continuar
		}

		if d.IsDir() {
			return nil
		}

		checked++

		info, err := d.Info()
		if err != nil {
			return nil
		}

		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err != nil {
				s.log.Warn("erro ao deletar arquivo expirado", zap.String("path", path), zap.Error(err))
			} else {
				deleted++
				s.log.Debug("arquivo expirado deletado", zap.String("path", path))
			}
		}

		return nil
	})

	if err != nil {
		s.log.Error("erro durante limpeza", zap.Error(err))
	}

	s.log.Info("limpeza de mídia concluída",
		zap.Int("checked", checked),
		zap.Int("deleted", deleted),
	)
}

func getExtensionFromMimetype(mimetype string) string {
	switch mimetype {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/3gpp":
		return ".3gp"
	case "audio/ogg":
		return ".ogg"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4":
		return ".m4a"
	case "application/pdf":
		return ".pdf"
	case "application/msword":
		return ".doc"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	default:
		return ".bin"
	}
}

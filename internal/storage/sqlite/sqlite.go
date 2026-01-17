package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
)

type DB struct {
	Conn *sql.DB
	log  *zap.Logger
}

func New(dataDir string, log *zap.Logger) (*DB, error) {
	// Garantir que o diretório existe
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("sqlite: criar diretório: %w", err)
	}

	dbPath := filepath.Join(dataDir, "apime.db")
	dsn := fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL", dbPath)

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: falha ao abrir: %w", err)
	}

	// Configurar pool de conexões
	db.SetMaxOpenConns(1) // SQLite não suporta múltiplas escritas simultâneas
	db.SetMaxIdleConns(1)

	if err := db.PingContext(context.Background()); err != nil {
		return nil, fmt.Errorf("sqlite: falha ao ping: %w", err)
	}

	log.Info("sqlite: conectado com sucesso",
		zap.String("path", dbPath),
	)

	return &DB{Conn: db, log: log}, nil
}

func (db *DB) Close() error {
	if db.Conn != nil {
		return db.Conn.Close()
	}
	return nil
}

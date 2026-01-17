package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/mattn/go-sqlite3"

	"github.com/open-apime/apime/internal/config"
)

func main() {
	migrationsDir := flag.String("migrations", "db/migrations/postgres", "Diretório de migrations PostgreSQL")
	migrationsSQLiteDir := flag.String("migrations-sqlite", "db/migrations/sqlite", "Diretório de migrations SQLite")
	seedsDir := flag.String("seeds", "db/seeds/postgres", "Diretório de seeds PostgreSQL")
	seedsSQLiteDir := flag.String("seeds-sqlite", "db/seeds/sqlite", "Diretório de seeds SQLite")
	withSeeds := flag.Bool("with-seeds", false, "Executar seeds após migrations")
	flag.Parse()

	cfg := config.Load()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	switch cfg.Storage.Driver {
	case "sqlite", "":
		log.Println("migrate: usando SQLite")
		runSQLiteMigrations(ctx, cfg, *migrationsSQLiteDir, *seedsSQLiteDir, *withSeeds)
	case "postgres":
		log.Println("migrate: usando PostgreSQL")
		runPostgresMigrations(ctx, cfg, *migrationsDir, *seedsDir, *withSeeds)
	default:
		log.Fatalf("migrate: driver desconhecido: %s", cfg.Storage.Driver)
	}
}

func runSQLiteMigrations(ctx context.Context, cfg config.Config, migrationsDir, seedsDir string, withSeeds bool) {
	if err := os.MkdirAll(cfg.Storage.DataDir, 0755); err != nil {
		log.Fatalf("migrate: erro ao criar diretório: %v", err)
	}

	dbPath := filepath.Join(cfg.Storage.DataDir, "apime.db")
	dsn := fmt.Sprintf("file:%s?_foreign_keys=on", dbPath)

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		log.Fatalf("migrate: falha ao abrir SQLite: %v", err)
	}
	defer db.Close()

	log.Printf("migrate: conectado ao SQLite em %s", dbPath)

	if err := ensureSchemaMigrationsSQLite(ctx, db); err != nil {
		log.Fatalf("migrate: falha ao preparar schema_migrations: %v", err)
	}

	if err := applySQLiteMigrations(ctx, db, migrationsDir); err != nil {
		log.Fatalf("migrate: erro ao aplicar migrations: %v", err)
	}

	if withSeeds {
		if err := runSQLiteSeeds(ctx, db, seedsDir); err != nil {
			log.Fatalf("migrate: erro ao executar seeds: %v", err)
		}
	}

	log.Println("migrate: concluído com sucesso.")
}

func ensureSchemaMigrationsSQLite(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT (datetime('now'))
		)
	`)
	return err
}

func applySQLiteMigrations(ctx context.Context, db *sql.DB, dir string) error {
	files, err := listSQLFiles(dir, ".up.sql")
	if err != nil {
		return fmt.Errorf("listar migrations: %w", err)
	}
	if len(files) == 0 {
		log.Printf("migrate: nenhum arquivo .up.sql encontrado em %s", dir)
		return nil
	}

	for _, file := range files {
		version := filepath.Base(file)
		applied, err := migrationAppliedSQLite(ctx, db, version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}

		log.Printf("migrate: aplicando %s ...", version)
		sqlStmt, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("ler %s: %w", version, err)
		}

		if err := execSQLiteBatch(ctx, db, string(sqlStmt)); err != nil {
			return fmt.Errorf("executar %s: %w", version, err)
		}

		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
			return fmt.Errorf("registrar %s: %w", version, err)
		}

		log.Printf("migrate: %s aplicado.", version)
	}
	return nil
}

func migrationAppliedSQLite(ctx context.Context, db *sql.DB, version string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&count); err != nil {
		return false, fmt.Errorf("verificar %s: %w", version, err)
	}
	return count > 0, nil
}

func execSQLiteBatch(ctx context.Context, db *sql.DB, statements string) error {
	stmts := strings.Split(statements, ";")
	for _, stmt := range stmts {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func runSQLiteSeeds(ctx context.Context, db *sql.DB, dir string) error {
	files, err := listSQLFiles(dir, ".sql")
	if err != nil {
		return fmt.Errorf("listar seeds: %w", err)
	}
	if len(files) == 0 {
		log.Printf("migrate: nenhum seed encontrado em %s", dir)
		return nil
	}

	for _, file := range files {
		sqlStmt, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("ler seed %s: %w", file, err)
		}
		if err := execSQLiteBatch(ctx, db, string(sqlStmt)); err != nil {
			return fmt.Errorf("executar seed %s: %w", file, err)
		}
		log.Printf("migrate: seed %s aplicado", filepath.Base(file))
	}
	return nil
}

func runPostgresMigrations(ctx context.Context, cfg config.Config, migrationsDir, seedsDir string, withSeeds bool) {
	pool, err := pgxpool.New(ctx, cfg.DB.DSN())
	if err != nil {
		log.Fatalf("migrate: falha ao conectar no banco: %v", err)
	}
	defer pool.Close()

	log.Println("migrate: conectado ao PostgreSQL, garantindo tabela de controle...")
	if err := ensureSchemaMigrations(ctx, pool); err != nil {
		log.Fatalf("migrate: falha ao preparar schema_migrations: %v", err)
	}

	if err := applyMigrations(ctx, pool, migrationsDir); err != nil {
		log.Fatalf("migrate: erro ao aplicar migrations: %v", err)
	}

	if withSeeds {
		if err := runSeeds(ctx, pool, seedsDir); err != nil {
			log.Fatalf("migrate: erro ao executar seeds: %v", err)
		}
	}

	log.Println("migrate: concluído com sucesso.")
}

func ensureSchemaMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	return err
}

func applyMigrations(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	files, err := listSQLFiles(dir, ".up.sql")
	if err != nil {
		return fmt.Errorf("listar migrations: %w", err)
	}
	if len(files) == 0 {
		log.Printf("migrate: nenhum arquivo .up.sql encontrado em %s", dir)
		return nil
	}

	for _, file := range files {
		version := filepath.Base(file)
		applied, err := migrationApplied(ctx, pool, version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}

		log.Printf("migrate: aplicando %s ...", version)
		sqlStmt, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("ler %s: %w", version, err)
		}

		if err := execSQL(ctx, pool, string(sqlStmt)); err != nil {
			return fmt.Errorf("executar %s: %w", version, err)
		}

		if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			return fmt.Errorf("registrar %s: %w", version, err)
		}

		log.Printf("migrate: %s aplicado.", version)
	}
	return nil
}

func runSeeds(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	files, err := listSQLFiles(dir, ".sql")
	if err != nil {
		return fmt.Errorf("listar seeds: %w", err)
	}
	if len(files) == 0 {
		log.Printf("migrate: nenhum seed encontrado em %s", dir)
		return nil
	}

	for _, file := range files {
		sqlStmt, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("ler seed %s: %w", file, err)
		}
		rowsAffected, err := execSQLWithRowsAffected(ctx, pool, string(sqlStmt))
		if err != nil {
			return fmt.Errorf("executar seed %s: %w", file, err)
		}
		if rowsAffected > 0 {
			log.Printf("migrate: seed %s aplicado (%d linhas inseridas)", filepath.Base(file), rowsAffected)
		}
	}
	return nil
}

func execSQL(ctx context.Context, pool *pgxpool.Pool, statement string) error {
	sql := strings.TrimSpace(statement)
	if sql == "" {
		return nil
	}
	ctxExec, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, err := pool.Exec(ctxExec, sql)
	return err
}

func execSQLWithRowsAffected(ctx context.Context, pool *pgxpool.Pool, statement string) (int64, error) {
	sql := strings.TrimSpace(statement)
	if sql == "" {
		return 0, nil
	}
	ctxExec, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	result, err := pool.Exec(ctxExec, sql)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func migrationApplied(ctx context.Context, pool *pgxpool.Pool, version string) (bool, error) {
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&exists); err != nil {
		return false, fmt.Errorf("verificar %s: %w", version, err)
	}
	return exists, nil
}

func listSQLFiles(dir, suffix string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if suffix != "" && !strings.HasSuffix(name, suffix) {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Strings(files)
	return files, nil
}

package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/open-apime/apime/internal/storage/model"
)

func newTestRepo(t *testing.T) *idempotencyRepo {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "test.db") + "?_journal_mode=WAL"
	conn, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	schema, err := os.ReadFile("../../../db/migrations/sqlite/000007_idempotency_keys.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := conn.Exec(string(schema)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewIdempotencyRepository(&DB{Conn: conn})
}

func record(key, hash string) model.IdempotencyRecord {
	now := time.Now()
	return model.IdempotencyRecord{
		InstanceID: "inst-1", Key: key, RequestHash: hash,
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
}

func TestTryAcquireOwnsKeyOnlyOnce(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	acquired, _, err := repo.TryAcquire(ctx, record("k1", "hash-a"))
	if err != nil || !acquired {
		t.Fatalf("primeira chamada deveria adquirir: acquired=%v err=%v", acquired, err)
	}

	acquired, existing, err := repo.TryAcquire(ctx, record("k1", "hash-a"))
	if err != nil {
		t.Fatalf("segunda chamada: %v", err)
	}
	if acquired {
		t.Fatal("segunda chamada nao deveria adquirir a mesma chave")
	}
	if existing.Status != model.IdempotencyStarted {
		t.Fatalf("esperava status started, veio %q", existing.Status)
	}
}

// The whole point of the primary key: concurrent callers must not both send.
func TestTryAcquireIsRaceSafe(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	const callers = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0

	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			acquired, _, err := repo.TryAcquire(ctx, record("race", "hash-a"))
			if err != nil {
				return
			}
			if acquired {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if won != 1 {
		t.Fatalf("exatamente um caller deveria adquirir a chave, mas %d adquiriram", won)
	}
}

func TestCompleteThenReplayKeepsResponse(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	if _, _, err := repo.TryAcquire(ctx, record("k2", "hash-a")); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := repo.Complete(ctx, "inst-1", "k2", 200, `{"data":{"messageId":"ABC"}}`); err != nil {
		t.Fatalf("complete: %v", err)
	}

	_, existing, err := repo.TryAcquire(ctx, record("k2", "hash-a"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if existing.Status != model.IdempotencyCompleted {
		t.Fatalf("esperava completed, veio %q", existing.Status)
	}
	if existing.ResponseStatus != 200 || existing.ResponseBody != `{"data":{"messageId":"ABC"}}` {
		t.Fatalf("resposta nao foi preservada: %d %q", existing.ResponseStatus, existing.ResponseBody)
	}
}

func TestReleaseFreesKeyForRetry(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	if _, _, err := repo.TryAcquire(ctx, record("k3", "hash-a")); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := repo.Release(ctx, "inst-1", "k3"); err != nil {
		t.Fatalf("release: %v", err)
	}

	acquired, _, err := repo.TryAcquire(ctx, record("k3", "hash-a"))
	if err != nil || !acquired {
		t.Fatalf("apos release a chave deveria estar livre: acquired=%v err=%v", acquired, err)
	}
}

func TestExpiredKeyIsReusable(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	old := record("k4", "hash-a")
	old.ExpiresAt = time.Now().Add(-1 * time.Hour)
	if _, _, err := repo.TryAcquire(ctx, old); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	acquired, _, err := repo.TryAcquire(ctx, record("k4", "hash-a"))
	if err != nil || !acquired {
		t.Fatalf("chave expirada deveria ser readquirida: acquired=%v err=%v", acquired, err)
	}
}

func TestDeleteExpiredRemovesOnlyOldKeys(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	live := record("live", "hash-a")
	expired := record("expired", "hash-a")
	expired.ExpiresAt = time.Now().Add(-1 * time.Hour)
	if _, _, err := repo.TryAcquire(ctx, live); err != nil {
		t.Fatalf("acquire live: %v", err)
	}
	if _, _, err := repo.TryAcquire(ctx, expired); err != nil {
		t.Fatalf("acquire expired: %v", err)
	}

	removed, err := repo.DeleteExpired(ctx, time.Now())
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if removed != 1 {
		t.Fatalf("esperava remover 1 chave, removeu %d", removed)
	}
	if _, err := repo.get(ctx, "inst-1", "live"); err != nil {
		t.Fatalf("chave viva foi removida: %v", err)
	}
}

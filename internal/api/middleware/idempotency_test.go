package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/storage/model"
)

// fakeRepo is an in-memory stand-in with the same atomic acquire semantics.
type fakeRepo struct {
	mu      sync.Mutex
	records map[string]model.IdempotencyRecord
	failAll bool
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{records: map[string]model.IdempotencyRecord{}}
}

func (f *fakeRepo) TryAcquire(_ context.Context, rec model.IdempotencyRecord) (bool, model.IdempotencyRecord, error) {
	if f.failAll {
		return false, model.IdempotencyRecord{}, context.DeadlineExceeded
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	k := rec.InstanceID + "|" + rec.Key
	if existing, ok := f.records[k]; ok {
		return false, existing, nil
	}
	rec.Status = model.IdempotencyStarted
	f.records[k] = rec
	return true, model.IdempotencyRecord{}, nil
}

func (f *fakeRepo) Complete(_ context.Context, instanceID, key string, status int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := instanceID + "|" + key
	rec := f.records[k]
	rec.Status = model.IdempotencyCompleted
	rec.ResponseStatus = status
	rec.ResponseBody = body
	f.records[k] = rec
	return nil
}

func (f *fakeRepo) Release(_ context.Context, instanceID, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.records, instanceID+"|"+key)
	return nil
}

func (f *fakeRepo) DeleteExpired(context.Context, time.Time) (int64, error) { return 0, nil }

func newRouter(repo *fakeRepo, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/send", Idempotency(repo, zap.NewNop()), handler)
	r.GET("/send", Idempotency(repo, zap.NewNop()), handler)
	return r
}

func post(r *gin.Engine, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/send", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set(IdempotencyHeader, key)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// The guarantee that keeps the four consumers working untouched: without the
// header nothing changes, and the handler runs on every call.
func TestWithoutHeaderEveryRequestReachesHandler(t *testing.T) {
	repo := newFakeRepo()
	calls := 0
	r := newRouter(repo, func(c *gin.Context) {
		calls++
		c.JSON(http.StatusOK, gin.H{"data": "sent"})
	})

	for i := 0; i < 3; i++ {
		if w := post(r, "", `{"to":"55"}`); w.Code != http.StatusOK {
			t.Fatalf("status %d", w.Code)
		}
	}
	if calls != 3 {
		t.Fatalf("sem header o handler deveria rodar 3 vezes, rodou %d", calls)
	}
	if len(repo.records) != 0 {
		t.Fatalf("sem header nada deveria ser gravado, gravou %d", len(repo.records))
	}
}

func TestRepeatedKeyReplaysInsteadOfSendingAgain(t *testing.T) {
	repo := newFakeRepo()
	calls := 0
	r := newRouter(repo, func(c *gin.Context) {
		calls++
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"messageId": "ABC"}})
	})

	first := post(r, "k1", `{"to":"55"}`)
	second := post(r, "k1", `{"to":"55"}`)

	if calls != 1 {
		t.Fatalf("a mensagem deveria ser enviada uma unica vez, foram %d", calls)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("replay devolveu corpo diferente:\n%s\n%s", first.Body.String(), second.Body.String())
	}
	if second.Header().Get(IdempotencyReplayHeader) != "true" {
		t.Fatal("replay deveria marcar X-Idempotent-Replay")
	}
}

func TestSameKeyWithDifferentBodyIsRejected(t *testing.T) {
	repo := newFakeRepo()
	r := newRouter(repo, func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"data": "sent"}) })

	post(r, "k1", `{"to":"55"}`)
	w := post(r, "k1", `{"to":"66"}`)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("esperava 422 para payload diferente, veio %d", w.Code)
	}
}

func TestConcurrentRepeatGetsConflict(t *testing.T) {
	repo := newFakeRepo()
	// Leaves the key in "started", as if the first request were still running.
	_, _, _ = repo.TryAcquire(context.Background(), model.IdempotencyRecord{
		InstanceID: "", Key: "k1", RequestHash: "whatever",
	})
	r := newRouter(repo, func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"data": "sent"}) })

	if w := post(r, "k1", `{"to":"55"}`); w.Code != http.StatusConflict {
		t.Fatalf("esperava 409 enquanto a original roda, veio %d", w.Code)
	}
}

// A rejected request never reached WhatsApp, so the caller may fix it and retry.
func TestClientErrorFreesTheKey(t *testing.T) {
	repo := newFakeRepo()
	fail := true
	calls := 0
	r := newRouter(repo, func(c *gin.Context) {
		calls++
		if fail {
			c.JSON(http.StatusBadRequest, gin.H{"error": "instância não conectada"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": "sent"})
	})

	if w := post(r, "k1", `{"to":"55"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("esperava 400, veio %d", w.Code)
	}
	fail = false
	if w := post(r, "k1", `{"to":"55"}`); w.Code != http.StatusOK {
		t.Fatalf("retry apos 400 deveria passar, veio %d", w.Code)
	}
	if calls != 2 {
		t.Fatalf("o handler deveria rodar nas duas vezes, rodou %d", calls)
	}
}

// A 5xx may have reached WhatsApp before failing, so it is replayed like any
// other stored result instead of sending a second time.
func TestServerErrorIsStoredAndReplayed(t *testing.T) {
	repo := newFakeRepo()
	calls := 0
	r := newRouter(repo, func(c *gin.Context) {
		calls++
		c.JSON(http.StatusInternalServerError, gin.H{"error": "falha"})
	})

	post(r, "k1", `{"to":"55"}`)
	w := post(r, "k1", `{"to":"55"}`)

	if calls != 1 {
		t.Fatalf("nao deveria reexecutar apos 5xx, rodou %d", calls)
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("esperava replay do 500, veio %d", w.Code)
	}
}

// Losing the store must not block sending: the request goes through.
func TestStoreFailureDoesNotBlockSend(t *testing.T) {
	repo := newFakeRepo()
	repo.failAll = true
	calls := 0
	r := newRouter(repo, func(c *gin.Context) {
		calls++
		c.JSON(http.StatusOK, gin.H{"data": "sent"})
	})

	if w := post(r, "k1", `{"to":"55"}`); w.Code != http.StatusOK {
		t.Fatalf("esperava 200 mesmo com store fora, veio %d", w.Code)
	}
	if calls != 1 {
		t.Fatalf("handler deveria rodar, rodou %d", calls)
	}
}

func TestGetIsNotGuarded(t *testing.T) {
	repo := newFakeRepo()
	calls := 0
	r := newRouter(repo, func(c *gin.Context) {
		calls++
		c.JSON(http.StatusOK, gin.H{"data": "list"})
	})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/send", nil)
		req.Header.Set(IdempotencyHeader, "k1")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d", w.Code)
		}
	}
	if calls != 2 {
		t.Fatalf("GET nao deveria ser guardado, rodou %d", calls)
	}
}

func TestOversizedKeyIsRejected(t *testing.T) {
	repo := newFakeRepo()
	r := newRouter(repo, func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"data": "sent"}) })

	if w := post(r, strings.Repeat("x", 256), `{"to":"55"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("esperava 400 para chave gigante, veio %d", w.Code)
	}
}

// The handler must still be able to read the body after the middleware hashes it.
func TestHandlerStillReadsBody(t *testing.T) {
	repo := newFakeRepo()
	var seen string
	r := newRouter(repo, func(c *gin.Context) {
		var body struct {
			To string `json:"to"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		seen = body.To
		c.JSON(http.StatusOK, gin.H{"data": "sent"})
	})

	post(r, "k1", `{"to":"5511999"}`)
	if seen != "5511999" {
		t.Fatalf("handler leu %q, esperava 5511999", seen)
	}
}

// gin.Recovery sits above this middleware, so a panic must not leave the key
// stuck in "started" and every later retry stuck on 409.
func TestPanicFreesTheKey(t *testing.T) {
	repo := newFakeRepo()
	boom := true
	calls := 0

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.POST("/send", Idempotency(repo, zap.NewNop()), func(c *gin.Context) {
		calls++
		if boom {
			panic("falha inesperada")
		}
		c.JSON(http.StatusOK, gin.H{"data": "sent"})
	})

	if w := post(r, "k1", `{"to":"55"}`); w.Code != http.StatusInternalServerError {
		t.Fatalf("esperava 500 do recovery, veio %d", w.Code)
	}
	if len(repo.records) != 0 {
		t.Fatalf("a chave deveria ter sido liberada, sobraram %d", len(repo.records))
	}

	boom = false
	if w := post(r, "k1", `{"to":"55"}`); w.Code != http.StatusOK {
		t.Fatalf("retry apos panico deveria passar, veio %d", w.Code)
	}
	if calls != 2 {
		t.Fatalf("handler deveria rodar duas vezes, rodou %d", calls)
	}
}

// The send path answers 503 "sessão não pronta, tente novamente" before the
// message reaches WhatsApp. Storing it would answer every retry with the same
// 503 for 24h, so the key has to be freed.
func TestSessionNotReadyFreesTheKey(t *testing.T) {
	repo := newFakeRepo()
	notReady := true
	calls := 0
	r := newRouter(repo, func(c *gin.Context) {
		calls++
		if notReady {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sessão não pronta, tente novamente"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": "sent"})
	})

	if w := post(r, "k1", `{"to":"55"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("esperava 503, veio %d", w.Code)
	}
	if len(repo.records) != 0 {
		t.Fatalf("a chave deveria ter sido liberada, sobraram %d", len(repo.records))
	}

	notReady = false
	if w := post(r, "k1", `{"to":"55"}`); w.Code != http.StatusOK {
		t.Fatalf("o retry com a mesma chave deveria passar, veio %d", w.Code)
	}
	if calls != 2 {
		t.Fatalf("o handler deveria rodar nas duas, rodou %d", calls)
	}
}

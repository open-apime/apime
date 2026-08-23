package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/storage/model"
)

const (
	IdempotencyHeader = "Idempotency-Key"
	// Tells the caller the response came from the store, not from a new send.
	IdempotencyReplayHeader = "X-Idempotent-Replay"

	idempotencyKeyMaxLen = 255
	IdempotencyTTL       = 24 * time.Hour

	// A media upload can carry up to 75MB and these hosts are short on RAM, so
	// multipart bodies are never buffered to be hashed. The key still protects
	// against a double send; only the "same key, different payload" check is
	// weaker for those routes.
	maxHashedBodyBytes = 1 << 20
)

// captureWriter keeps a copy of the response so it can be replayed later.
type captureWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *captureWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *captureWriter) WriteString(s string) (int, error) {
	w.body.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

// Idempotency makes a retried POST safe: the first request stores its result
// under the caller's key and a repeat replays it, instead of sending the same
// message to WhatsApp twice. Follows draft-ietf-httpapi-idempotency-key-header:
// 409 while the original still runs, 422 when the key comes back with a
// different payload.
//
// Without the header the request goes straight through, so callers that do not
// send a key behave exactly as before.
func Idempotency(repo storage.IdempotencyRepository, log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := strings.TrimSpace(c.GetHeader(IdempotencyHeader))
		if key == "" || repo == nil || c.Request.Method != http.MethodPost {
			c.Next()
			return
		}
		if len(key) > idempotencyKeyMaxLen {
			c.AbortWithStatusJSON(http.StatusBadRequest,
				gin.H{"error": "Idempotency-Key excede 255 caracteres"})
			return
		}

		instanceID := c.GetString("instanceID")
		if instanceID == "" {
			instanceID = c.Param("id")
		}

		hash, err := hashRequest(c)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "corpo da requisição inválido"})
			return
		}

		now := time.Now()
		acquired, existing, err := repo.TryAcquire(c.Request.Context(), model.IdempotencyRecord{
			InstanceID:  instanceID,
			Key:         key,
			RequestHash: hash,
			CreatedAt:   now,
			ExpiresAt:   now.Add(IdempotencyTTL),
		})
		if err != nil {
			// The store is not the reason to drop a send: log and let it through.
			log.Warn("idempotência indisponível, seguindo sem ela",
				zap.String("instance", instanceID), zap.Error(err))
			c.Next()
			return
		}

		if !acquired {
			replay(c, existing, hash)
			return
		}

		writer := &captureWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
		c.Writer = writer

		// Deferred so a panic in the handler still frees the key. gin.Recovery
		// sits above this middleware, so without the defer the key would stay
		// "started" and every retry would get a 409 until the TTL expired.
		defer func() {
			// context.Background: the request context is already cancelled when
			// the client hangs up, and the result still has to be recorded.
			ctx := context.Background()

			if !c.Writer.Written() {
				if err := repo.Release(ctx, instanceID, key); err != nil {
					log.Warn("erro ao liberar chave de idempotência", zap.Error(err))
				}
				return
			}

			// A 4xx means the message never left, so the key is freed and the
			// caller can fix the payload and retry. A 5xx is ambiguous (it may
			// have reached WhatsApp before failing), so it is stored and
			// replayed like a success.
			//
			// 503 is the exception: the send path returns it when the session is
			// not ready, before reaching WhatsApp, and the body tells the caller
			// to try again. Storing it would answer every retry with the same
			// 503 until the key expired, making that instruction impossible.
			status := c.Writer.Status()
			if (status >= 400 && status < 500) || status == http.StatusServiceUnavailable {
				if err := repo.Release(ctx, instanceID, key); err != nil {
					log.Warn("erro ao liberar chave de idempotência", zap.Error(err))
				}
				return
			}
			if err := repo.Complete(ctx, instanceID, key, status, writer.body.String()); err != nil {
				log.Warn("erro ao gravar resultado de idempotência", zap.Error(err))
			}
		}()

		c.Next()
	}
}

func replay(c *gin.Context, rec model.IdempotencyRecord, hash string) {
	if rec.Status == model.IdempotencyStarted {
		c.AbortWithStatusJSON(http.StatusConflict,
			gin.H{"error": "requisição com esta Idempotency-Key ainda está em andamento"})
		return
	}
	if rec.RequestHash != hash {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity,
			gin.H{"error": "Idempotency-Key já usada com outro conteúdo"})
		return
	}
	c.Header(IdempotencyReplayHeader, "true")
	c.Data(rec.ResponseStatus, "application/json; charset=utf-8", []byte(rec.ResponseBody))
	c.Abort()
}

// hashRequest fingerprints the request so the same key coming back with a
// different payload can be rejected. Multipart is fingerprinted by route only,
// to avoid holding a 75MB upload in memory.
func hashRequest(c *gin.Context) (string, error) {
	sum := sha256.New()
	sum.Write([]byte(c.Request.Method))
	sum.Write([]byte(c.FullPath()))

	contentType := c.GetHeader("Content-Type")
	if strings.HasPrefix(contentType, "multipart/") || c.Request.ContentLength > maxHashedBodyBytes {
		return hex.EncodeToString(sum.Sum(nil)), nil
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxHashedBodyBytes))
	if err != nil {
		return "", err
	}
	// The handler still needs to read it.
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	sum.Write(body)
	return hex.EncodeToString(sum.Sum(nil)), nil
}

package delivery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"
)

type Delivery struct {
	client     *http.Client
	log        *zap.Logger
	maxRetries int
}

func NewDelivery(log *zap.Logger, maxRetries int) *Delivery {
	return &Delivery{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		log:        log,
		maxRetries: maxRetries,
	}
}

func (d *Delivery) Deliver(ctx context.Context, url string, secret string, event map[string]interface{}) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("delivery: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("delivery: new request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ApiMe/1.0")

	// Adicionar assinatura HMAC se secret estiver configurado
	if secret != "" {
		signature := d.generateSignature(payload, secret)
		req.Header.Set("X-ApiMe-Signature", signature)
	}

	var lastErr error
	for attempt := 0; attempt <= d.maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt) * time.Second
			d.log.Info("delivery: retry", zap.Int("attempt", attempt), zap.Duration("backoff", backoff))
			time.Sleep(backoff)
		}

		resp, err := d.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("delivery: request: %w", err)
			continue
		}

		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			d.log.Info("delivery: sucesso", zap.String("webhook", url), zap.Int("status", resp.StatusCode))
			return nil
		}

		lastErr = fmt.Errorf("delivery: status %d", resp.StatusCode)
	}

	return fmt.Errorf("delivery: falhou após %d tentativas: %w", d.maxRetries+1, lastErr)
}

func (d *Delivery) generateSignature(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (d *Delivery) VerifySignature(payload []byte, signature, secret string) bool {
	expected := d.generateSignature(payload, secret)
	return hmac.Equal([]byte(signature), []byte(expected))
}

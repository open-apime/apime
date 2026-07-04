package dashboard

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (h *Handler) docsPage(c *gin.Context) {
	preview, exists := h.readOpenAPIPreview()
	data := map[string]any{
		"HasSpec": exists,
		"Preview": preview,
	}
	page := h.pageData(c, "", "docs_content", data)
	c.HTML(http.StatusOK, "layout", page)
}

func (h *Handler) downloadOpenAPI(c *gin.Context) {
	path := h.openAPIPath()
	if path == "" {
		c.String(http.StatusNotFound, "Arquivo não disponível")
		return
	}
	c.FileAttachment(path, filepath.Base(path))
}

func (h *Handler) openAPIPath() string {
	if _, err := os.Stat("openapi.yaml"); err == nil {
		return "openapi.yaml"
	}
	if h.docsDir != "" {
		path := filepath.Join(h.docsDir, "openapi.yaml")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func (h *Handler) readOpenAPIPreview() (string, bool) {
	path := h.openAPIPath()
	if path == "" {
		return "", false
	}
	content, err := os.ReadFile(path)
	if err != nil {
		h.logger.Warn("não foi possível ler openapi", zap.Error(err))
		return "", false
	}
	text := string(content)
	if len(text) > 2000 {
		text = text[:2000] + "\n..."
	}
	return text, true
}

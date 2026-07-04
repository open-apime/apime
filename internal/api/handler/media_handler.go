package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/open-apime/apime/internal/storage/media"
)

// MediaHandler handles media requests
type MediaHandler struct {
	storage *media.Storage
}

// NewMediaHandler creates a new media handler
func NewMediaHandler(storage *media.Storage) *MediaHandler {
	return &MediaHandler{
		storage: storage,
	}
}

// GetMedia serves a media file by ID
// GET /api/media/:instanceId/:mediaId
func (h *MediaHandler) GetMedia(c *gin.Context) {
	instanceID := c.Param("instanceId")
	mediaID := c.Param("mediaId")

	if instanceID == "" || mediaID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "instanceId e mediaId são obrigatórios"})
		return
	}

	if !h.storage.Exists(instanceID, mediaID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "mídia não encontrada ou expirada"})
		return
	}

	data, err := h.storage.Get(c.Request.Context(), instanceID, mediaID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	contentType := getContentTypeFromFilename(mediaID)

	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Cross-Origin-Resource-Policy", "cross-origin")

	c.Data(http.StatusOK, contentType, data)
}

// getContentTypeFromFilename returns the content-type based on the file extension
func getContentTypeFromFilename(filename string) string {
	if len(filename) < 4 {
		return "application/octet-stream"
	}

	switch {
	case endsWith(filename, ".jpg"), endsWith(filename, ".jpeg"):
		return "image/jpeg"
	case endsWith(filename, ".png"):
		return "image/png"
	case endsWith(filename, ".gif"):
		return "image/gif"
	case endsWith(filename, ".webp"):
		return "image/webp"
	case endsWith(filename, ".mp4"):
		return "video/mp4"
	case endsWith(filename, ".3gp"):
		return "video/3gpp"
	case endsWith(filename, ".ogg"):
		return "audio/ogg"
	case endsWith(filename, ".mp3"):
		return "audio/mpeg"
	case endsWith(filename, ".m4a"):
		return "audio/mp4"
	case endsWith(filename, ".pdf"):
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

func endsWith(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}

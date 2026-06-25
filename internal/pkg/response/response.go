package response

import "github.com/gin-gonic/gin"

func Success(c *gin.Context, status int, payload interface{}) {
	c.JSON(status, gin.H{"data": payload})
}

func Error(c *gin.Context, status int, err error) {
	// Registra o erro no contexto do gin para que o middleware de Sentry
	// (SentryReport) capture o erro real via CaptureError, em vez de apenas a
	// mensagem genérica "HTTP <status>".
	if err != nil {
		_ = c.Error(err)
	}
	c.JSON(status, gin.H{
		"error": err.Error(),
	})
}

func ErrorWithMessage(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"error": message,
	})
}

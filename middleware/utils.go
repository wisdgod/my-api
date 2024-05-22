package middleware

import (
	"one-api/common"

	"github.com/gin-gonic/gin"
)

func abortWithOpenAiMessage(c *gin.Context, statusCode int, message string) {
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"message": common.MessageWithRequestId(message, c.GetString(common.RequestIdKey)),
			"type":    "my_api_error",
		},
	})
	c.Abort()
	common.LogError(c.Request.Context(), message)
}

func abortWithMidjourneyMessage(c *gin.Context, statusCode int, code int, description string) {
	c.JSON(statusCode, gin.H{
		"description": description,
		"type":        "my_api_error",
		"code":        code,
	})
	c.Abort()
	common.LogError(c.Request.Context(), description)
}

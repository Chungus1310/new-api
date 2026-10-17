package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// LimitRequestBody caps bodies on routes registered before the relay's
// DecompressRequestMiddleware, which otherwise read unbounded input: the payment
// webhooks buffer the whole body before checking its signature.
func LimitRequestBody(maxMB int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxMB<<20)
		}
		c.Next()
	}
}

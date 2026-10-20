package middleware

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

const corsOwnDomain = "unorouter.com"

// Bearer works from any origin; only our own sites may send the session cookie.
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		header := c.Writer.Header()
		header.Add("Vary", "Origin")
		if isOwnCORSOrigin(origin) {
			header.Set("Access-Control-Allow-Origin", origin)
			header.Set("Access-Control-Allow-Credentials", "true")
		} else {
			header.Set("Access-Control-Allow-Origin", "*")
			header.Del("Access-Control-Allow-Credentials")
		}
		if c.Request.Method != http.MethodOptions || c.GetHeader("Access-Control-Request-Method") == "" {
			c.Next()
			return
		}
		header.Add("Vary", "Access-Control-Request-Method")
		header.Add("Vary", "Access-Control-Request-Headers")
		header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		// Echoed rather than "*": the wildcard never covers Authorization.
		if requested := c.GetHeader("Access-Control-Request-Headers"); requested != "" {
			header.Set("Access-Control-Allow-Headers", requested)
		}
		header.Set("Access-Control-Max-Age", "43200")
		c.AbortWithStatus(http.StatusNoContent)
	}
}

func isOwnCORSOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Port() != "" || u.Path != "" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == corsOwnDomain || strings.HasSuffix(host, "."+corsOwnDomain)
}

func Version() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-New-Api-Version", common.Version)
		c.Next()
	}
}

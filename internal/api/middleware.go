package api

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/contextos/contextos/internal/auth"
	"github.com/contextos/contextos/internal/types"
	"github.com/gin-gonic/gin"
)

// ServiceAuthMiddleware validates the service API key and extracts
// tenant/user identifiers into a RequestContext stored in gin.Context.
// In devMode, API key validation is skipped.
func ServiceAuthMiddleware(keyMgr *auth.APIKeyManager, devMode bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !devMode {
			apiKey := c.GetHeader("X-API-Key")
			if apiKey == "" {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"code":    types.ErrUnauthorized,
					"message": "missing X-API-Key header",
				})
				return
			}

			valid, err := keyMgr.Verify(c.Request.Context(), apiKey)
			if err != nil || !valid {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"code":    types.ErrUnauthorized,
					"message": "invalid or revoked API key",
				})
				return
			}
		}

		tenantID := c.GetHeader("X-Tenant-ID")
		if tenantID == "" {
			tenantID = "default"
		}

		userID := c.GetHeader("X-User-ID")
		if userID == "" {
			userID = "default"
		}

		rc := types.RequestContext{
			TenantID: tenantID,
			UserID:   userID,
		}
		c.Set("request_context", rc)
		c.Next()
	}
}

// AdminAuthMiddleware validates the admin session token from the
// Authorization header and stores the admin session in gin.Context.
func AdminAuthMiddleware(adminAuth *auth.AdminAuth) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code":    types.ErrUnauthorized,
				"message": "missing Authorization header",
			})
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code":    types.ErrUnauthorized,
				"message": "malformed Authorization header, expected Bearer <token>",
			})
			return
		}

		token := parts[1]
		session, err := adminAuth.VerifySession(c.Request.Context(), token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code":    types.ErrUnauthorized,
				"message": "invalid or expired admin session",
			})
			return
		}

		c.Set("admin_session", session)
		c.Next()
	}
}

// RequestIDMiddleware generates a UUID for each request, sets it as the
// X-Request-Id response header, and stores it in gin.Context.
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := generateRequestID()
		c.Header("X-Request-Id", id)
		c.Set("request_id", id)
		c.Next()
	}
}

// ProcessTimeMiddleware records the request processing duration and sets
// it as the X-Process-Time response header in milliseconds.
func ProcessTimeMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		duration := time.Since(start)
		c.Header("X-Process-Time", fmt.Sprintf("%.3fms", float64(duration.Microseconds())/1000.0))
	}
}

// GetRequestContext retrieves the RequestContext stored by ServiceAuthMiddleware.
func GetRequestContext(c *gin.Context) types.RequestContext {
	val, exists := c.Get("request_context")
	if !exists {
		return types.RequestContext{TenantID: "default", UserID: "default"}
	}
	rc, ok := val.(types.RequestContext)
	if !ok {
		return types.RequestContext{TenantID: "default", UserID: "default"}
	}
	return rc
}

// GetAdminSession retrieves the AdminSession stored by AdminAuthMiddleware.
func GetAdminSession(c *gin.Context) *types.AdminSession {
	val, exists := c.Get("admin_session")
	if !exists {
		return nil
	}
	session, ok := val.(*types.AdminSession)
	if !ok {
		return nil
	}
	return session
}

// GetRequestID retrieves the request ID stored by RequestIDMiddleware.
func GetRequestID(c *gin.Context) string {
	val, _ := c.Get("request_id")
	id, _ := val.(string)
	return id
}

// generateRequestID produces a UUID v4 string.
func generateRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

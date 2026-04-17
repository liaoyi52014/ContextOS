package api

import (
	"github.com/gin-gonic/gin"
)

func (s *Server) auditAdmin(c *gin.Context, action, targetType, targetID string, detail map[string]interface{}) {
	if s.audit == nil {
		return
	}
	admin := GetAdminSession(c)
	userID := ""
	if admin != nil {
		userID = admin.UserID
	}
	_ = s.audit.Log(c.Request.Context(), "default", userID, action, targetType, targetID, detail, GetRequestID(c))
}

func (s *Server) auditRequest(c *gin.Context, action, targetType, targetID string, detail map[string]interface{}) {
	if s.audit == nil {
		return
	}
	rc := GetRequestContext(c)
	_ = s.audit.Log(c.Request.Context(), rc.TenantID, rc.UserID, action, targetType, targetID, detail, GetRequestID(c))
}

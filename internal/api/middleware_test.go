package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/contextos/contextos/internal/types"
	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// --- ServiceAuthMiddleware tests ---

func TestServiceAuthMiddleware_DevMode_SkipsValidation(t *testing.T) {
	r := gin.New()
	r.Use(ServiceAuthMiddleware(nil, true))
	r.GET("/test", func(c *gin.Context) {
		rc := GetRequestContext(c)
		c.JSON(http.StatusOK, rc)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var rc types.RequestContext
	if err := json.Unmarshal(w.Body.Bytes(), &rc); err != nil {
		t.Fatal(err)
	}
	if rc.TenantID != "default" || rc.UserID != "default" {
		t.Fatalf("expected default/default, got %s/%s", rc.TenantID, rc.UserID)
	}
}

func TestServiceAuthMiddleware_DevMode_ExtractsHeaders(t *testing.T) {
	r := gin.New()
	r.Use(ServiceAuthMiddleware(nil, true))
	r.GET("/test", func(c *gin.Context) {
		rc := GetRequestContext(c)
		c.JSON(http.StatusOK, rc)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Tenant-ID", "acme")
	req.Header.Set("X-User-ID", "user_123")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var rc types.RequestContext
	if err := json.Unmarshal(w.Body.Bytes(), &rc); err != nil {
		t.Fatal(err)
	}
	if rc.TenantID != "acme" {
		t.Fatalf("expected tenant acme, got %s", rc.TenantID)
	}
	if rc.UserID != "user_123" {
		t.Fatalf("expected user user_123, got %s", rc.UserID)
	}
}

func TestServiceAuthMiddleware_ProdMode_MissingKey(t *testing.T) {
	r := gin.New()
	r.Use(ServiceAuthMiddleware(nil, false))
	r.GET("/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}

	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "UNAUTHORIZED" {
		t.Fatalf("expected UNAUTHORIZED code, got %s", body["code"])
	}
}

// --- AdminAuthMiddleware tests ---

func TestAdminAuthMiddleware_MissingHeader(t *testing.T) {
	r := gin.New()
	r.Use(AdminAuthMiddleware(nil))
	r.GET("/admin", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAdminAuthMiddleware_MalformedHeader(t *testing.T) {
	r := gin.New()
	r.Use(AdminAuthMiddleware(nil))
	r.GET("/admin", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	tests := []string{
		"Basic abc123",
		"Bearer",
		"Bearer ",
		"justtoken",
	}

	for _, header := range tests {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/admin", nil)
		req.Header.Set("Authorization", header)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("header %q: expected 401, got %d", header, w.Code)
		}
	}
}

// --- RequestIDMiddleware tests ---

func TestRequestIDMiddleware_SetsHeader(t *testing.T) {
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.GET("/test", func(c *gin.Context) {
		id := GetRequestID(c)
		if id == "" {
			t.Fatal("request ID should not be empty")
		}
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	xReqID := w.Header().Get("X-Request-Id")
	if xReqID == "" {
		t.Fatal("X-Request-Id header should be set")
	}
	// UUID v4 format: 8-4-4-4-12
	if len(xReqID) != 36 {
		t.Fatalf("expected UUID length 36, got %d", len(xReqID))
	}
}

func TestRequestIDMiddleware_UniquePerRequest(t *testing.T) {
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.GET("/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	ids := make(map[string]bool)
	for i := 0; i < 10; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/test", nil)
		r.ServeHTTP(w, req)
		id := w.Header().Get("X-Request-Id")
		if ids[id] {
			t.Fatalf("duplicate request ID: %s", id)
		}
		ids[id] = true
	}
}

// --- ProcessTimeMiddleware tests ---

func TestProcessTimeMiddleware_SetsHeader(t *testing.T) {
	r := gin.New()
	r.Use(ProcessTimeMiddleware())
	r.GET("/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	pt := w.Header().Get("X-Process-Time")
	if pt == "" {
		t.Fatal("X-Process-Time header should be set")
	}
	if len(pt) < 2 || pt[len(pt)-2:] != "ms" {
		t.Fatalf("expected X-Process-Time to end with 'ms', got %s", pt)
	}
}

// --- Helper function tests ---

func TestGetRequestContext_NoContext(t *testing.T) {
	r := gin.New()
	r.GET("/test", func(c *gin.Context) {
		rc := GetRequestContext(c)
		if rc.TenantID != "default" || rc.UserID != "default" {
			t.Fatalf("expected default/default, got %s/%s", rc.TenantID, rc.UserID)
		}
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)
}

func TestGetAdminSession_NoSession(t *testing.T) {
	r := gin.New()
	r.GET("/test", func(c *gin.Context) {
		session := GetAdminSession(c)
		if session != nil {
			t.Fatal("expected nil session")
		}
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)
}

func TestGetRequestID_NoID(t *testing.T) {
	r := gin.New()
	r.GET("/test", func(c *gin.Context) {
		id := GetRequestID(c)
		if id != "" {
			t.Fatalf("expected empty string, got %s", id)
		}
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)
}

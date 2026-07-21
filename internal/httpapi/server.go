package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
)

// Server holds the HTTP layer's dependencies and builds the router.
type Server struct {
	cfg    config.Settings
	store  *db.Store
	guards *auth.Guards
}

// NewServer wires the HTTP layer.
func NewServer(cfg config.Settings, store *db.Store, guards *auth.Guards) *Server {
	return &Server{cfg: cfg, store: store, guards: guards}
}

// Router builds the Gin engine with CORS, health, and every router mounted at
// the same prefixes as the FastAPI app.
func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(s.cors())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "environment": s.cfg.Environment})
	})

	s.registerPricing(r)
	s.registerConfig(r)
	s.registerAdmin(r)
	s.registerProjects(r)
	s.registerBrands(r)
	s.registerInternal(r)

	return r
}

// cors mirrors the FastAPI CORSMiddleware: the configured origins, credentials
// allowed, all methods and headers.
func (s *Server) cors() gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(s.cfg.CORSOrigins))
	for _, o := range s.cfg.CORSOrigins {
		allowed[o] = struct{}{}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if _, ok := allowed[origin]; ok {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", "*")
			c.Header("Access-Control-Allow-Headers", "*")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

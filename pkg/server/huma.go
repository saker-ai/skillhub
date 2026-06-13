package server

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humagin"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

type HumaSetup struct {
	API    huma.API
	config huma.Config
}

type docAdapter struct {
	huma.Adapter
}

func (docAdapter) Handle(*huma.Operation, func(huma.Context)) {}

type docAPI struct {
	huma.API
	adapter docAdapter
}

func (d docAPI) Adapter() huma.Adapter {
	return d.adapter
}

func NewDocOnlyAPI(api huma.API) huma.API {
	return docAPI{API: api, adapter: docAdapter{api.Adapter()}}
}

func newHumaSetup(engine *gin.Engine) HumaSetup {
	config := huma.DefaultConfig("SkillHub API", "1.0.0")
	config.Info.Description = "SkillHub registry API for publishing, discovering, and downloading reusable agent skills and plugins."
	config.Info.Contact = &huma.Contact{
		Name: "Saker",
		URL:  "https://github.com/saker-ai/skillhub",
	}
	config.Info.License = &huma.License{
		Name: "MIT",
		URL:  "https://opensource.org/licenses/MIT",
	}
	config.OpenAPI.Servers = []*huma.Server{
		{URL: "http://localhost:17020"},
	}

	if config.Components == nil {
		config.Components = &huma.Components{}
	}
	config.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"BearerAuth": {
			Type:         "http",
			Scheme:       "bearer",
			BearerFormat: "SkillHub token",
			Description:  `Bearer token from /api/v1/tokens, /api/v1/namespaces/{slug}/tokens, or device auth.`,
		},
		"CookieAuth": {
			Type:        "apiKey",
			In:          "cookie",
			Name:        "session_token",
			Description: "Web session cookie set by /login or OAuth callback.",
		},
	}

	api := humagin.New(engine, config)
	return HumaSetup{API: api, config: config}
}

func (s *Server) registerHumaDocs(r gin.IRouter) {
	engine, ok := r.(*gin.Engine)
	if !ok {
		return
	}

	setup := newHumaSetup(engine)
	registerOpenAPIDocs(setup.API)

	// Backward-compatible aliases for clients that consumed the previous
	// hand-written spec paths. Huma owns /openapi.json, /docs, and /schemas.
	engine.GET("/api/v1/openapi.json", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=300")
		c.JSON(http.StatusOK, setup.API.OpenAPI())
	})
	engine.GET("/api/v1/openapi.yaml", func(c *gin.Context) {
		body, err := yaml.Marshal(setup.API.OpenAPI())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "openapi_yaml_marshal"})
			return
		}
		c.Header("Cache-Control", "public, max-age=300")
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", body)
	})
	engine.GET("/api/docs", func(c *gin.Context) {
		c.Request.URL.Path = "/docs"
		engine.HandleContext(c)
	})
}

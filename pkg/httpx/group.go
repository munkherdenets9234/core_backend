package httpx

import "github.com/gin-gonic/gin"

// G wraps *gin.RouterGroup so every route registered through it is wrapped in
// H automatically.
//
// Without it, each registration site reads httpx.H(h.list) and the wrapper is
// something a new route can forget — and a forgotten wrapper is a handler
// whose errors are silently discarded, returning 200 with an empty body. With
// G the wrapping is structural: a route registered on a G is wrapped, full
// stop.
//
//	g := httpx.Wrap(base.Group("/blogs"))
//	g.GET("", h.List)        // H applied automatically
//	g.POST("", h.Create)
type G struct{ *gin.RouterGroup }

// Wrap returns a G for the given router group.
func Wrap(r *gin.RouterGroup) *G { return &G{r} }

func (g *G) GET(path string, fn HandlerFunc)    { g.RouterGroup.GET(path, H(fn)) }
func (g *G) POST(path string, fn HandlerFunc)   { g.RouterGroup.POST(path, H(fn)) }
func (g *G) PUT(path string, fn HandlerFunc)    { g.RouterGroup.PUT(path, H(fn)) }
func (g *G) PATCH(path string, fn HandlerFunc)  { g.RouterGroup.PATCH(path, H(fn)) }
func (g *G) DELETE(path string, fn HandlerFunc) { g.RouterGroup.DELETE(path, H(fn)) }

// Group returns a new G rooted at path, inheriting the parent's middleware.
func (g *G) Group(path string, middleware ...gin.HandlerFunc) *G {
	return &G{g.RouterGroup.Group(path, middleware...)}
}

// Use adds middleware to the group and returns g for chaining.
func (g *G) Use(middleware ...gin.HandlerFunc) *G {
	g.RouterGroup.Use(middleware...)
	return g
}

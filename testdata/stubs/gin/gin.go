// Package gin is a minimal stub of github.com/gin-gonic/gin carrying only
// the signatures softmap's entrypoint matchers key on (see
// internal/entrypoints). Bodies are empty; the fixture is never executed.
package gin

type HandlerFunc func(*Context)

type Context struct{}

func (c *Context) JSON(code int, obj any)    {}
func (c *Context) Param(key string) string   { return "" }
func (c *Context) BindJSON(obj any) error    { return nil }
func (c *Context) String(code int, s string) {}
func (c *Context) Status(code int)           {}

type RouterGroup struct{}

func (g *RouterGroup) GET(path string, handlers ...HandlerFunc)                {}
func (g *RouterGroup) POST(path string, handlers ...HandlerFunc)               {}
func (g *RouterGroup) PUT(path string, handlers ...HandlerFunc)                {}
func (g *RouterGroup) DELETE(path string, handlers ...HandlerFunc)             {}
func (g *RouterGroup) PATCH(path string, handlers ...HandlerFunc)              {}
func (g *RouterGroup) Handle(method, path string, handlers ...HandlerFunc)     {}
func (g *RouterGroup) Group(path string, handlers ...HandlerFunc) *RouterGroup { return g }

type Engine struct{ RouterGroup }

func New() *Engine                         { return &Engine{} }
func Default() *Engine                     { return &Engine{} }
func (e *Engine) Run(addr ...string) error { return nil }

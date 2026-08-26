// Package echo is a minimal stub of github.com/labstack/echo/v4 carrying
// the shapes softmap matches on: route registration for entrypoint
// discovery and Context response methods for HTTP-response guard exits.
package echo

type HandlerFunc func(c Context) error
type MiddlewareFunc func(next HandlerFunc) HandlerFunc

type Context interface {
	Param(name string) string
	Bind(i any) error
	JSON(code int, i any) error
	String(code int, s string) error
	NoContent(code int) error
}

type Echo struct{}

func New() *Echo { return &Echo{} }

func (e *Echo) GET(path string, h HandlerFunc, m ...MiddlewareFunc)  {}
func (e *Echo) POST(path string, h HandlerFunc, m ...MiddlewareFunc) {}
func (e *Echo) Group(prefix string, m ...MiddlewareFunc) *Group      { return &Group{} }

type Group struct{}

func (g *Group) GET(path string, h HandlerFunc, m ...MiddlewareFunc)  {}
func (g *Group) POST(path string, h HandlerFunc, m ...MiddlewareFunc) {}

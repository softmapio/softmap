// Package fiber is a minimal stub of github.com/gofiber/fiber/v2 carrying
// only the shapes softmap matches on: route registration for entrypoint
// discovery (see internal/entrypoints) and Ctx response methods for
// HTTP-response guard exits. Bodies are empty; the fixture is never executed.
//
// The v2 handler signature takes *Ctx, and a registration takes a variadic
// handler list whose last element is the endpoint. Group and Route hand back
// the Router interface, so routes registered on a group are
// interface-dispatched calls.
package fiber

type Ctx struct{}

func (c *Ctx) Params(key string, defaultValue ...string) string { return "" }
func (c *Ctx) BodyParser(out any) error                         { return nil }
func (c *Ctx) JSON(data any, ctype ...string) error             { return nil }
func (c *Ctx) SendStatus(status int) error                      { return nil }
func (c *Ctx) SendString(body string) error                     { return nil }
func (c *Ctx) Status(status int) *Ctx                           { return c }

type Handler func(*Ctx) error

// Router is implemented by both *App and *Group.
type Router interface {
	Get(path string, handlers ...Handler) Router
	Post(path string, handlers ...Handler) Router
	Put(path string, handlers ...Handler) Router
	Patch(path string, handlers ...Handler) Router
	Delete(path string, handlers ...Handler) Router
	Head(path string, handlers ...Handler) Router
	Options(path string, handlers ...Handler) Router
	All(path string, handlers ...Handler) Router
	Add(method, path string, handlers ...Handler) Router
	Group(prefix string, handlers ...Handler) Router
	Route(prefix string, fn func(router Router), name ...string) Router
	Use(args ...any) Router
}

type App struct{}

type Config struct{ AppName string }

func New(config ...Config) *App { return &App{} }

func (app *App) Get(path string, handlers ...Handler) Router     { return app }
func (app *App) Post(path string, handlers ...Handler) Router    { return app }
func (app *App) Put(path string, handlers ...Handler) Router     { return app }
func (app *App) Patch(path string, handlers ...Handler) Router   { return app }
func (app *App) Delete(path string, handlers ...Handler) Router  { return app }
func (app *App) Head(path string, handlers ...Handler) Router    { return app }
func (app *App) Options(path string, handlers ...Handler) Router { return app }
func (app *App) All(path string, handlers ...Handler) Router     { return app }

func (app *App) Add(method, path string, handlers ...Handler) Router { return app }
func (app *App) Group(prefix string, handlers ...Handler) Router     { return &Group{} }
func (app *App) Use(args ...any) Router                              { return app }
func (app *App) Listen(addr string) error                            { return nil }

func (app *App) Route(prefix string, fn func(router Router), name ...string) Router {
	return app
}

type Group struct{}

func (g *Group) Get(path string, handlers ...Handler) Router     { return g }
func (g *Group) Post(path string, handlers ...Handler) Router    { return g }
func (g *Group) Put(path string, handlers ...Handler) Router     { return g }
func (g *Group) Patch(path string, handlers ...Handler) Router   { return g }
func (g *Group) Delete(path string, handlers ...Handler) Router  { return g }
func (g *Group) Head(path string, handlers ...Handler) Router    { return g }
func (g *Group) Options(path string, handlers ...Handler) Router { return g }
func (g *Group) All(path string, handlers ...Handler) Router     { return g }

func (g *Group) Add(method, path string, handlers ...Handler) Router { return g }
func (g *Group) Group(prefix string, handlers ...Handler) Router     { return &Group{} }
func (g *Group) Use(args ...any) Router                              { return g }

func (g *Group) Route(prefix string, fn func(router Router), name ...string) Router {
	return g
}

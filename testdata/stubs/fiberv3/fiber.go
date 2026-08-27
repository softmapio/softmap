// Package fiber is a minimal stub of github.com/gofiber/fiber/v3 carrying
// only the shapes softmap matches on: route registration for entrypoint
// discovery (see internal/entrypoints) and Ctx response methods for
// HTTP-response guard exits. Bodies are empty; the fixture is never executed.
//
// v3 differs from v2 in exactly the ways the matcher cares about: Ctx is an
// interface rather than a pointer, a registration takes its endpoint handler
// as the first argument with middleware after it (v2 takes a variadic list
// whose last element is the endpoint), and Add takes several methods at once.
package fiber

type Ctx interface {
	Params(key string, defaultValue ...string) string
	JSON(data any, ctype ...string) error
	SendStatus(status int) error
	SendString(body string) error
}

type Handler func(c Ctx) error

// Router is implemented by both *App and *Group.
type Router interface {
	Get(path string, handler any, handlers ...any) Router
	Post(path string, handler any, handlers ...any) Router
	Put(path string, handler any, handlers ...any) Router
	Patch(path string, handler any, handlers ...any) Router
	Delete(path string, handler any, handlers ...any) Router
	Head(path string, handler any, handlers ...any) Router
	Options(path string, handler any, handlers ...any) Router
	All(path string, handler any, handlers ...any) Router
	Add(methods []string, path string, handler any, handlers ...any) Router
	Group(prefix string, handlers ...any) Router
	Route(prefix string, fn func(router Router), name ...string) Router
	Use(args ...any) Router
}

type App struct{}

type Config struct{ AppName string }

func New(config ...Config) *App { return &App{} }

func (app *App) Get(path string, handler any, handlers ...any) Router     { return app }
func (app *App) Post(path string, handler any, handlers ...any) Router    { return app }
func (app *App) Put(path string, handler any, handlers ...any) Router     { return app }
func (app *App) Patch(path string, handler any, handlers ...any) Router   { return app }
func (app *App) Delete(path string, handler any, handlers ...any) Router  { return app }
func (app *App) Head(path string, handler any, handlers ...any) Router    { return app }
func (app *App) Options(path string, handler any, handlers ...any) Router { return app }
func (app *App) All(path string, handler any, handlers ...any) Router     { return app }

func (app *App) Group(prefix string, handlers ...any) Router { return &Group{} }
func (app *App) Use(args ...any) Router                      { return app }
func (app *App) Listen(addr string) error                    { return nil }

func (app *App) Add(methods []string, path string, handler any, handlers ...any) Router {
	return app
}

func (app *App) Route(prefix string, fn func(router Router), name ...string) Router {
	return app
}

type Group struct{}

func (g *Group) Get(path string, handler any, handlers ...any) Router     { return g }
func (g *Group) Post(path string, handler any, handlers ...any) Router    { return g }
func (g *Group) Put(path string, handler any, handlers ...any) Router     { return g }
func (g *Group) Patch(path string, handler any, handlers ...any) Router   { return g }
func (g *Group) Delete(path string, handler any, handlers ...any) Router  { return g }
func (g *Group) Head(path string, handler any, handlers ...any) Router    { return g }
func (g *Group) Options(path string, handler any, handlers ...any) Router { return g }
func (g *Group) All(path string, handler any, handlers ...any) Router     { return g }

func (g *Group) Group(prefix string, handlers ...any) Router { return &Group{} }
func (g *Group) Use(args ...any) Router                      { return g }

func (g *Group) Add(methods []string, path string, handler any, handlers ...any) Router {
	return g
}

func (g *Group) Route(prefix string, fn func(router Router), name ...string) Router {
	return g
}

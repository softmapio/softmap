// Command gorillashop is the gorilla/mux fixture. It deliberately mirrors
// the architecture from the first field report (issue #1): the router is
// built in a method, subrouters are created with PathPrefix().Subrouter()
// and passed into helper methods, one subrouter carries middleware, and some
// routes register through the route-builder chain instead of HandleFunc.
package main

import (
	"net/http"

	"github.com/gorilla/mux"
)

type Application struct{}

func (app *Application) router() *mux.Router { return mux.NewRouter() }

func (app *Application) Serve() error {
	router := app.router()

	// Base API subrouter, passed into a helper method.
	apiRouter := router.PathPrefix("/api").Subrouter()
	app.authRouter(apiRouter)

	// Nested subrouter with middleware; middleware is not an entrypoint.
	adminRouter := apiRouter.PathPrefix("/admin").Subrouter()
	adminRouter.Use(app.authMiddleware)
	app.adminRouter(adminRouter)

	// Directly attached handler on a subrouter, method chained after.
	apiRouter.HandleFunc("/bot", app.bot).Methods("GET")

	// Additional sub-routes registered via a separate helper.
	app.telegramRouter(apiRouter)

	// Route-builder chains on the root router.
	router.Path("/healthz").HandlerFunc(app.healthz)
	router.Methods("POST").Path("/orders").HandlerFunc(app.createOrder)

	// A prefix route holding an http.Handler value.
	apiRouter.PathPrefix("/files/").Handler(http.HandlerFunc(app.serveFiles))

	// StrictSlash is a pure passthrough and must not break the prefix chain.
	v2 := apiRouter.PathPrefix("/v2").Subrouter().StrictSlash(true)
	v2.HandleFunc("/ping", app.pingV2).Methods("GET")

	// Methods separated from the registration by another builder link.
	router.HandleFunc("/export", app.export).Name("export").Methods("PUT")

	// A subrouter kept in a struct field, assigned once by a constructor.
	newReports(apiRouter.PathPrefix("/reports").Subrouter()).register()

	// A subrouter captured by a closure.
	metrics := apiRouter.PathPrefix("/metrics").Subrouter()
	setup := func() {
		metrics.HandleFunc("/latency", app.latency).Methods("GET")
	}
	setup()

	return http.ListenAndServe(":8080", router)
}

type reports struct {
	router *mux.Router
	app    *Application
}

func newReports(r *mux.Router) *reports { return &reports{router: r, app: &Application{}} }

func (rp *reports) register() {
	rp.router.HandleFunc("/daily", rp.app.dailyReport).Methods("GET")
}

func (app *Application) authRouter(r *mux.Router) {
	r.HandleFunc("/login", app.login).Methods("POST")
	r.HandleFunc("/me", app.me).Methods("GET")
}

func (app *Application) adminRouter(r *mux.Router) {
	r.HandleFunc("/stats", app.stats).Methods("GET")
	// Builder chain inside a helper: methods before path.
	r.Methods("DELETE").Path("/users/{id}").HandlerFunc(app.deleteUser)
}

func (app *Application) telegramRouter(r *mux.Router) {
	tg := r.PathPrefix("/telegram").Subrouter()
	tg.HandleFunc("/webhook", app.telegramWebhook).Methods("POST")
}

func (app *Application) authMiddleware(next http.Handler) http.Handler { return next }

func (app *Application) bot(w http.ResponseWriter, r *http.Request)             {}
func (app *Application) pingV2(w http.ResponseWriter, r *http.Request)          {}
func (app *Application) export(w http.ResponseWriter, r *http.Request)          {}
func (app *Application) dailyReport(w http.ResponseWriter, r *http.Request)     {}
func (app *Application) latency(w http.ResponseWriter, r *http.Request)         {}
func (app *Application) healthz(w http.ResponseWriter, r *http.Request)         {}
func (app *Application) createOrder(w http.ResponseWriter, r *http.Request)     {}
func (app *Application) serveFiles(w http.ResponseWriter, r *http.Request)      {}
func (app *Application) login(w http.ResponseWriter, r *http.Request)           {}
func (app *Application) me(w http.ResponseWriter, r *http.Request)              {}
func (app *Application) stats(w http.ResponseWriter, r *http.Request)           {}
func (app *Application) deleteUser(w http.ResponseWriter, r *http.Request)      {}
func (app *Application) telegramWebhook(w http.ResponseWriter, r *http.Request) {}

func main() { _ = (&Application{}).Serve() }

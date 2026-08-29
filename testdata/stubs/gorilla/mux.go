// Package mux is a minimal stub of github.com/gorilla/mux carrying only the
// shapes softmap matches on: route registration, the route-builder chain,
// and subrouters for entrypoint discovery. Bodies are empty; the fixture is
// never executed.
package mux

import "net/http"

type MiddlewareFunc func(http.Handler) http.Handler

type Router struct{}

func NewRouter() *Router { return &Router{} }

func (r *Router) Handle(path string, handler http.Handler) *Route { return &Route{} }
func (r *Router) HandleFunc(path string, f func(http.ResponseWriter, *http.Request)) *Route {
	return &Route{}
}
func (r *Router) Path(tpl string) *Route           { return &Route{} }
func (r *Router) PathPrefix(tpl string) *Route     { return &Route{} }
func (r *Router) Methods(methods ...string) *Route { return &Route{} }
func (r *Router) NewRoute() *Route                 { return &Route{} }
func (r *Router) Use(mwf ...MiddlewareFunc)        {}
func (r *Router) StrictSlash(value bool) *Router   { return r }
func (r *Router) SkipClean(value bool) *Router     { return r }
func (r *Router) UseEncodedPath() *Router          { return r }

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {}

type Route struct{}

func (r *Route) Subrouter() *Router { return &Router{} }

func (r *Route) Handler(handler http.Handler) *Route                           { return r }
func (r *Route) HandlerFunc(f func(http.ResponseWriter, *http.Request)) *Route { return r }
func (r *Route) Path(tpl string) *Route                                        { return r }
func (r *Route) PathPrefix(tpl string) *Route                                  { return r }
func (r *Route) Methods(methods ...string) *Route                              { return r }
func (r *Route) Name(name string) *Route                                       { return r }
func (r *Route) Queries(pairs ...string) *Route                                { return r }
func (r *Route) Host(tpl string) *Route                                        { return r }
func (r *Route) Schemes(schemes ...string) *Route                              { return r }
func (r *Route) Headers(pairs ...string) *Route                                { return r }

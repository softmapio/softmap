// Package chi is a minimal stub of github.com/go-chi/chi/v5 with the exact
// registration signatures the entrypoint matcher recognizes.
package chi

import "net/http"

type Router interface {
	Route(pattern string, fn func(r Router)) Router
	Group(fn func(r Router)) Router
	Get(pattern string, h http.HandlerFunc)
	Post(pattern string, h http.HandlerFunc)
}

type Mux struct{}

func NewRouter() *Mux { return &Mux{} }

func (m *Mux) Route(pattern string, fn func(r Router)) Router { fn(m); return m }
func (m *Mux) Group(fn func(r Router)) Router                 { fn(m); return m }
func (m *Mux) Get(pattern string, h http.HandlerFunc)         {}
func (m *Mux) Post(pattern string, h http.HandlerFunc)        {}

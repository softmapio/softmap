// Package grpc is a minimal stub of google.golang.org/grpc carrying only
// the shapes softmap matches on: ClientConn.Invoke for outgoing-call
// effects and the Register*Server / ServiceRegistrar pattern for
// entrypoint discovery.
package grpc

import "context"

type CallOption interface{}

type ClientConn struct{}

func (cc *ClientConn) Invoke(ctx context.Context, method string, args, reply any, opts ...CallOption) error {
	return nil
}

type ServiceDesc struct{ ServiceName string }

type ServiceRegistrar interface {
	RegisterService(desc *ServiceDesc, impl any)
}

type Server struct{}

func NewServer() *Server                                { return &Server{} }
func (s *Server) RegisterService(d *ServiceDesc, i any) {}
func (s *Server) Serve(l any) error                     { return nil }

// Package grpcserver exposes the toyshop service over gRPC: discovery must
// find (*Server).GetOrder through the RegisterOrdersServer call, and the
// upstream partner lookup must appear as a grpc effect.
package grpcserver

import (
	"context"

	grpc "google.golang.org/grpc"

	"example.com/toyshop/grpcapi"
	"example.com/toyshop/service"
)

type Server struct {
	grpcapi.UnimplementedOrdersServer
	svc     *service.Service
	partner *grpcapi.OrdersClient
}

func New(svc *service.Service, partner *grpcapi.OrdersClient) *Server {
	return &Server{svc: svc, partner: partner}
}

// Value receiver on purpose: resolving it through the *Server passed to
// Register goes via a synthetic indirection wrapper, which discovery must
// canonicalize to this declared method (a real-world regression).
func (s Server) GetOrder(ctx context.Context, req *grpcapi.GetOrderReq) (*grpcapi.GetOrderResp, error) {
	o, err := s.svc.GetOrder(ctx, req.Id)
	if err != nil {
		// Not ours: ask the partner shop.
		return s.partner.GetOrder(ctx, req)
	}
	return &grpcapi.GetOrderResp{Item: o.Item}, nil
}

func Run(svc *service.Service, cc *grpc.ClientConn) error {
	g := grpc.NewServer()
	grpcapi.RegisterOrdersServer(g, New(svc, grpcapi.NewOrdersClient(cc)))
	return g.Serve(nil)
}

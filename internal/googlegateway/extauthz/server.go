// Package extauthz is the Envoy ext_authz v3 wire adapter for the Google Agent
// Gateway authorization callout. It is the ONLY part of the integration that
// depends on gRPC / go-control-plane; the decision logic lives in the parent
// googlegateway package so it stays dependency-free and unit-testable.
//
// Google Agent Gateway's authorization extension (wireFormat EXT_AUTHZ_GRPC)
// makes a real-time gRPC Check() call to this server for every agent tool call
// routed through the gateway. OK => allow (the gateway forwards the tool call);
// PERMISSION_DENIED => deny (403 + reason). Fail-closed on anything unparseable.
//
// Build/run needs two modules not otherwise used by Atryum:
//
//	go get google.golang.org/grpc \
//	       github.com/envoyproxy/go-control-plane/envoy/service/auth/v3 \
//	       github.com/envoyproxy/go-control-plane/envoy/type/v3
//	go mod tidy
package extauthz

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	rpccode "google.golang.org/genproto/googleapis/rpc/code"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"atryum/internal/googlegateway"
)

// Server implements envoy.service.auth.v3.Authorization, delegating each Check
// to the transport-agnostic googlegateway.Service.
type Server struct {
	authv3.UnimplementedAuthorizationServer
	svc  *googlegateway.Service
	addr string
}

// New builds the ext_authz server over a decision service. addr is the gRPC
// listen address (e.g. ":8090") Agent Gateway's authz extension dials.
func New(svc *googlegateway.Service, addr string) *Server {
	return &Server{svc: svc, addr: addr}
}

// Serve starts the gRPC server and blocks until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	lis, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("google agent gateway callout listen on %s: %w", s.addr, err)
	}
	gs := grpc.NewServer()
	authv3.RegisterAuthorizationServer(gs, s)
	// gRPC health service so a Cloud Run / NEG health check on the callout
	// backend passes (grpc.health.v1).
	hs := health.NewServer()
	healthpb.RegisterHealthServer(gs, hs)
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	go func() {
		<-ctx.Done()
		hs.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		gs.GracefulStop()
	}()
	slog.Info("google agent gateway ext_authz callout listening", "addr", s.addr)
	return gs.Serve(lis)
}

// Check is the ext_authz entrypoint invoked by Agent Gateway per tool call.
func (s *Server) Check(ctx context.Context, req *authv3.CheckRequest) (*authv3.CheckResponse, error) {
	httpReq := req.GetAttributes().GetRequest().GetHttp()
	tc := googlegateway.ExtractToolCall(
		req.GetAttributes().GetContextExtensions(),
		httpReq.GetHeaders(),
		[]byte(httpReq.GetBody()),
	)
	if tc.Tool == "" {
		// Unattributable call: fail closed rather than wave it through.
		return denied("atryum: could not identify a tool in the gateway request"), nil
	}
	d := s.svc.Decide(ctx, tc)
	if d.Allow {
		return allowed(), nil
	}
	return denied(d.Message), nil
}

func allowed() *authv3.CheckResponse {
	return &authv3.CheckResponse{
		Status:       &rpcstatus.Status{Code: int32(rpccode.Code_OK)},
		HttpResponse: &authv3.CheckResponse_OkResponse{OkResponse: &authv3.OkHttpResponse{}},
	}
}

func denied(msg string) *authv3.CheckResponse {
	return &authv3.CheckResponse{
		Status: &rpcstatus.Status{Code: int32(rpccode.Code_PERMISSION_DENIED)},
		HttpResponse: &authv3.CheckResponse_DeniedResponse{
			DeniedResponse: &authv3.DeniedHttpResponse{
				Status: &typev3.HttpStatus{Code: typev3.StatusCode_Forbidden},
				Body:   msg,
			},
		},
	}
}

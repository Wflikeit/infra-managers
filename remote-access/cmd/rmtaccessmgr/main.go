// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os/signal"
	"sync"
	"syscall"

	resourcev1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/client"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/flags"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/logging"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/metrics"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/oam"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/policy/rbac"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/tracing"
	"github.com/open-edge-platform/infra-managers/remote-access/internal/common"
	"github.com/open-edge-platform/infra-managers/remote-access/internal/handlers"
	pb "github.com/open-edge-platform/infra-managers/remote-access/pkg/api/rmtaccessmgr/v1"
	"github.com/open-edge-platform/infra-managers/remote-access/pkg/clients"
	"github.com/open-edge-platform/infra-managers/remote-access/pkg/config"
	rmtAccessMgr "github.com/open-edge-platform/infra-managers/remote-access/pkg/rmtaccessconfmgr"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var zlog = logging.GetLogger("RemoteAccessConfigManagerMain")

var (
	servaddr             = flag.String(flags.ServerAddress, "0.0.0.0:50005", flags.ServerAddressDescription)
	invsvcaddr           = flag.String(client.InventoryAddress, "localhost:50051", client.InventoryAddressDescription)
	oamservaddr          = flag.String(oam.OamServerAddress, "", oam.OamServerAddressDescription)
	insecureGrpc         = flag.Bool(client.InsecureGrpc, true, client.InsecureGrpcDescription)
	caCertPath           = flag.String(client.CaCertPath, "", client.CaCertPathDescription)
	tlsCertPath          = flag.String(client.TLSCertPath, "", client.TLSCertPathDescription)
	tlsKeyPath           = flag.String(client.TLSKeyPath, "", client.TLSKeyPathDescription)
	enableTracing        = flag.Bool(tracing.EnableTracing, false, tracing.EnableTracingDescription)
	traceURL             = flag.String(tracing.TraceURL, "", tracing.TraceURLDescription)
	enableAuth           = flag.Bool(rbac.EnableAuth, true, rbac.EnableAuthDescription)
	rbacRules            = flag.String(rbac.RbacRules, "/rego/authz.rego", rbac.RbacRulesDescription)
	invCacheUUIDEnable   = flag.Bool(client.InvCacheUUIDEnable, false, client.InvCacheUUIDEnableDescription)
	invCacheStaleTimeout = flag.Duration(
		client.InvCacheStaleTimeout, client.InvCacheStaleTimeoutDefault, client.InvCacheStaleTimeoutDescription)
	invCacheStaleTimeoutOffset = flag.Uint(
		client.InvCacheStaleTimeoutOffset, client.InvCacheStaleTimeoutOffsetDefault, client.InvCacheStaleTimeoutOffsetDescription)

	enableMetrics  = flag.Bool(metrics.EnableMetrics, false, metrics.EnableMetricsDescription)
	metricsAddress = flag.String(metrics.MetricsAddress, metrics.MetricsAddressDefault, metrics.MetricsAddressDescription)

	// Inventory client flags
	inventoryTimeout        = flag.Duration(common.InventoryTimeout, common.DefaultInventoryTimeoutDuration, common.InventoryTimeoutDescription)
	listAllInventoryTimeout = flag.Duration(common.ListAllInventoryTimeout, common.DefaultListAllInventoryTimeoutDuration, common.ListAllInventoryTimeoutDescription)

	// Northbound handler flags
	reconcileTickerPeriod = flag.Duration(common.ReconcileTickerPeriod, common.DefaultReconcileTickerPeriodDuration, common.ReconcileTickerPeriodDescription)
	reconcileParallelism  = flag.Int(common.ReconcileParallelism, common.DefaultReconcileParallelism, common.ReconcileParallelismDescription)
)

type server struct {
	pb.UnimplementedRmtaccessmgrServiceServer
	mu sync.RWMutex
	db map[string]*resourcev1.RemoteAccessConfiguration
}

func newServer() *server {
	return &server{db: make(map[string]*resourcev1.RemoteAccessConfiguration)}
}

// GetAgentSpec — used by agent to retrieve connection spec
func (s *server) GetAgentSpec(
	ctx context.Context,
	req *pb.GetRemoteAccessConfigByGuidRequest,
) (*pb.GetResourceAccessConfigResponse, error) {
	if req == nil || req.Uuid == "" {
		return nil, status.Error(codes.InvalidArgument, "guid is required")
	}

	s.mu.RLock()
	a, ok := s.db[req.GetUuid()]
	s.mu.RUnlock()
	if !ok {
		return nil, status.Error(codes.NotFound, "resource access not found")
	}

	spec := &pb.AgentRemoteAccessSpec{
		RemoteAccessProxyEndpoint: a.GetProxyHost(),
		SessionToken:              a.GetSessionToken(),
		ReverseBindPort:           a.GetLocalPort(),
		TargetHost:                a.GetTargetHost(),
		TargetPort:                a.GetTargetPort(),
		SshUser:                   a.GetUser(),
		ExpirationTimestamp:       a.GetExpirationTimestamp(),
		Uuid:                      a.GetInstance().GetHost().GetUuid(),
	}

	log.Printf("📦 Returned AgentSpec: reverse_port=%d, uuid=%s", spec.ReverseBindPort)

	return &pb.GetResourceAccessConfigResponse{Spec: spec}, nil
}

func main() {
	flag.Parse()

	conf := config.RemoteAccessConfigMgrConfig{
		EnableTracing:           *enableTracing,
		EnableMetrics:           *enableMetrics,
		TraceURL:                *traceURL,
		InventoryAddr:           *invsvcaddr,
		CACertPath:              *caCertPath,
		TLSKeyPath:              *tlsKeyPath,
		TLSCertPath:             *tlsCertPath,
		InsecureGRPC:            *insecureGrpc,
		EnableUUIDCache:         *invCacheUUIDEnable,
		UUIDCacheTTL:            *invCacheStaleTimeout,
		UUIDCacheTTLOffset:      int(*invCacheStaleTimeoutOffset),
		InventoryTimeout:        *inventoryTimeout,
		ListAllInventoryTimeout: *listAllInventoryTimeout,
		ReconcileTickerPeriod:   *reconcileTickerPeriod,
		ReconcileParallelism:    *reconcileParallelism,
	}

	if err := conf.Validate(); err != nil {
		zlog.Fatal().Err(err).Msgf("Failed to start due to invalid configuration: %v", conf)
	}

	zlog.Info().Msgf("Starting Remote Access Manager conf %v", conf)

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	wg := &sync.WaitGroup{}

	coreInv, events, err := rmtAccessMgr.StartInvGrpcCli(rootCtx, wg, conf)
	if err != nil {
		zlog.Fatal().Err(err).Msg("failed to start inventory client")
	}

	raInv, err := clients.NewRAInventoryClient(coreInv, events)
	if err != nil {
		zlog.Fatal().Err(err).Msg("failed to create RA inventory wrapper")
	}

	// 3) NB handler (reconcileAll + event loop + ticker fallback)
	nbh, err := handlers.NewNBHandler(raInv, conf.EnableTracing, conf.ReconcileTickerPeriod, conf.ReconcileParallelism, conf.InventoryTimeout, conf.ListAllInventoryTimeout)
	if err != nil {
		zlog.Fatal().Err(err).Msg("failed to create NB handler")
	}
	if err := nbh.Start(); err != nil {
		zlog.Fatal().Err(err).Msg("failed to start NB handler")
	}
	// shutdown NB handler
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-rootCtx.Done()
		nbh.Stop()
	}()

	// 4) Southbound gRPC (agent polling)
	//lis, err := net.Listen("tcp", ":50051")
	lis, err := net.Listen("tcp", *servaddr) // np. 0.0.0.0:50001
	if err != nil {
		zlog.Fatal().Err(err).Msgf("failed to listen on %s", *servaddr)
	}
	if err := rmtAccessMgr.StartGrpcSrv(rootCtx, wg, lis, raInv, conf.InventoryTimeout,
		rmtAccessMgr.EnableTracing(*enableTracing),
		//rmtAccessMgr.EnableAuth(*enableAuth),
		//rmtAccessMgr.WithRbacRulesPath(*rbacRules),
		rmtAccessMgr.EnableMetrics(*enableMetrics),
		rmtAccessMgr.WithMetricsAddress(*metricsAddress),
	); err != nil {
		zlog.Fatal().Err(err).Msg("failed to start grpc server")
	}

	<-rootCtx.Done()
	zlog.Info().Msg("Shutdown signal received, waiting for goroutines...")
	wg.Wait()
	zlog.Info().Msg("RemoteAccessManager stopped")
}

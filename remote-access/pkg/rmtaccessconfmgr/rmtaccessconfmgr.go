// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package rmtaccessconfmgr

import (
	"log"
	"net"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	inv_v1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/inventory/v1"
	inv_client "github.com/open-edge-platform/infra-core/inventory/v2/pkg/client"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/logging"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/metrics"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/tracing"
	pb "github.com/open-edge-platform/infra-managers/remote-access/pkg/api/rmtaccessmgr/v1"
	"github.com/open-edge-platform/infra-managers/remote-access/pkg/clients"
	"github.com/open-edge-platform/infra-managers/remote-access/pkg/config"
	"golang.org/x/net/context"
)

const (
	backoffInterval = 5 * time.Second
	backoffRetries  = uint64(5)
	// eventsWatcherBufSize is the buffer size for the events channel.
	eventsWatcherBufSize = 10
)

var zlog = logging.GetLogger("RemoteAccessManager")

type Options struct {
	enableTracing bool
	enableMetrics bool
	metricsAddr   string
}

type Option func(*Options)

func EnableTracing(v bool) Option {
	return func(o *Options) { o.enableTracing = v }
}

func EnableMetrics(enable bool) Option {
	return func(o *Options) {
		o.enableMetrics = enable
	}
}

func WithMetricsAddress(metricsAddress string) Option {
	return func(o *Options) {
		o.metricsAddr = metricsAddress
	}
}

func parseOptions(opts ...Option) *Options {
	o := &Options{}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

func StartGrpcSrv(
	ctx context.Context,
	wg *sync.WaitGroup,
	lis net.Listener,
	invClient *clients.RmtAccessInventoryClient,
	inventoryTimeout time.Duration,
	opts ...Option,
) error {
	options := parseOptions(opts...)

	var grpcOpts []grpc.ServerOption
	var unaryInts []grpc.UnaryServerInterceptor

	srvMetrics := metrics.GetServerMetricsWithLatency()
	if options.enableMetrics {
		zlog.Info().Msgf("Metrics exporter is enabled")
		unaryInts = append(unaryInts, srvMetrics.UnaryServerInterceptor())
	}

	if options.enableTracing {
		grpcOpts = tracing.EnableGrpcServerTracing(grpcOpts)
	}

	if len(unaryInts) > 0 {
		grpcOpts = append(grpcOpts, grpc.ChainUnaryInterceptor(unaryInts...))
	}

	s := grpc.NewServer(grpcOpts...)

	pb.RegisterRmtaccessmgrServiceServer(
		s,
		NewServer(invClient, inventoryTimeout),
	)
	reflection.Register(s)

	if options.enableMetrics {
		srvMetrics.InitializeMetrics(s)
		metrics.StartMetricsExporter(
			[]prometheus.Collector{metrics.GetClientMetricsWithLatency(), srvMetrics},
			metrics.WithListenAddress(options.metricsAddr),
		)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		zlog.Info().Msgf("Serving RemoteAccessManager gRPC on %s", lis.Addr())
		log.Println("🧠 Manager gRPC listening on :50051")
		_ = s.Serve(lis)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		zlog.Info().Msg("Stopping RemoteAccessManager gRPC server")
		s.GracefulStop() // albo Stop() jeśli chcesz hard stop
	}()

	return nil
}

func StartInvGrpcCli(
	ctx context.Context,
	wg *sync.WaitGroup,
	conf config.RemoteAccessConfigMgrConfig,
) (inv_client.TenantAwareInventoryClient, chan *inv_client.WatchEvents, error) {

	resourceKinds := []inv_v1.ResourceKind{
		inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF,
	}

	events := make(chan *inv_client.WatchEvents, eventsWatcherBufSize)

	cfg := inv_client.InventoryClientConfig{
		Name:                      "rmtaccessmgr",
		Address:                   conf.InventoryAddr,
		Events:                    events,
		EnableRegisterRetry:       false,
		AbortOnUnknownClientError: true,
		ClientKind:                inv_v1.ClientKind_CLIENT_KIND_RESOURCE_MANAGER,
		ResourceKinds:             resourceKinds,
		EnableTracing:             conf.EnableTracing,
		EnableMetrics:             conf.EnableMetrics,
		Wg:                        wg,
		SecurityCfg: &inv_client.SecurityConfig{
			CaPath:   conf.CACertPath,
			KeyPath:  conf.TLSKeyPath,
			CertPath: conf.TLSCertPath,
			Insecure: conf.InsecureGRPC,
		},
		ClientCache: inv_client.InvClientCacheConfig{
			EnableUUIDCache: conf.EnableUUIDCache,
			StaleTime:       conf.UUIDCacheTTL,
			StateTimeOffset: conf.UUIDCacheTTLOffset,
		},
	}

	gcli, err := inv_client.NewTenantAwareInventoryClient(ctx, cfg)
	if err != nil {
		zlog.InfraSec().InfraErr(err).Msg("Cannot create new inventory client")
		return nil, nil, err
	}

	zlog.InfraSec().Info().Msg("Inventory client started")
	return gcli, events, nil
}

// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	chserver "github.com/jpillora/chisel/server"
	"github.com/prometheus/client_golang/prometheus"

	inv_client "github.com/open-edge-platform/infra-core/inventory/v2/pkg/client"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/logging"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/metrics"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/oam"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/tracing"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/chiselauth"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/clients"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/common"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/handlers"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/reconcilers"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/wsterm"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/pkg/config"
)

var (
	name                    = "RemoteAccessProxy"
	zlog                    = logging.GetLogger(name + "Main")
	inventoryAddress        = flag.String(inv_client.InventoryAddress, "localhost:50051", inv_client.InventoryAddressDescription)
	oamservaddr             = flag.String(oam.OamServerAddress, "", oam.OamServerAddressDescription)
	enableTracing           = flag.Bool(tracing.EnableTracing, false, tracing.EnableTracingDescription)
	enableMetrics           = flag.Bool(metrics.EnableMetrics, false, metrics.EnableMetricsDescription)
	metricsAddress          = flag.String(metrics.MetricsAddress, metrics.MetricsAddressDefault, metrics.MetricsAddressDescription)
	traceURL                = flag.String(tracing.TraceURL, "", tracing.TraceURLDescription)
	insecureGrpc            = flag.Bool(inv_client.InsecureGrpc, true, inv_client.InsecureGrpcDescription)
	caCertPath              = flag.String(inv_client.CaCertPath, "", inv_client.CaCertPathDescription)
	tlsCertPath             = flag.String(inv_client.TLSCertPath, "", inv_client.TLSCertPathDescription)
	tlsKeyPath              = flag.String(inv_client.TLSKeyPath, "", inv_client.TLSKeyPathDescription)
	inventoryTimeout        = flag.Duration(common.InventoryTimeout, common.DefaultInventoryTimeout, common.InventoryTimeoutDescription)
	listAllInventoryTimeout = flag.Duration(common.ListAllInventoryTimeout, common.DefaultListAllInventoryTimeout, common.ListAllInventoryTimeoutDescription)
	reconcileTickerPeriod   = flag.Duration(common.ReconcileTickerPeriod, common.DefaultReconcileTickerPeriod, common.ReconcileTickerPeriodDescription)
	reconcileParallelism    = flag.Int(common.ReconcileParallelism, common.DefaultReconcileParallelism, common.ReconcileParallelismDescription)
	wsAddr                  = flag.String(common.WebSocketAddr, common.DefaultWebSocketAddr, common.WebSocketAddrDescription)
	chiselBindAddr          = flag.String(common.ChiselBindAddr, common.DefaultChiselBindAddr, common.ChiselBindAddrDescription)
	chiselPort              = flag.String(common.ChiselPort, common.DefaultChiselPort, common.ChiselPortDescription)
	chiselKeySeed           = flag.String(common.ChiselKeySeed, common.DefaultChiselKeySeed, common.ChiselKeySeedDescription)
	chiselKeepAlive         = flag.Duration(common.ChiselKeepAlive, common.DefaultChiselKeepAlive, common.ChiselKeepAliveDescription)
	reverseSSHAddr          = flag.String(common.ReverseSSHAddr, common.DefaultReverseSSHAddr, common.ReverseSSHAddrDescription)
	reverseSSHWaitTimeout   = flag.Duration(common.ReverseSSHWaitTimeout, common.DefaultReverseSSHWaitTimeout, common.ReverseSSHWaitTimeoutDescription)
	sshPrivateKeyPath       = flag.String("sshPrivateKeyPath", "", "Path to SSH private key used by /term (optional)")
	sshPassword             = flag.String("sshPassword", "zaq12wsx", "Fallback SSH password used by /term when key auth is unavailable")
	invCacheUUIDEnable      = flag.Bool(inv_client.InvCacheUUIDEnable, false, inv_client.InvCacheUUIDEnableDescription)
	invCacheStaleTimeout    = flag.Duration(
		inv_client.InvCacheStaleTimeout, inv_client.InvCacheStaleTimeoutDefault, inv_client.InvCacheStaleTimeoutDescription)
	invCacheStaleTimeoutOffset = flag.Uint(
		inv_client.InvCacheStaleTimeoutOffset, inv_client.InvCacheStaleTimeoutOffsetDefault, inv_client.InvCacheStaleTimeoutOffsetDescription)
	wg        = sync.WaitGroup{}
	readyChan = make(chan bool, 1)
	termChan  = make(chan bool, 1)
	sigChan   = make(chan os.Signal, 1)
)

var (
	RepoURL   = "https://github.com/open-edge-platform/infra-managers/remote-access-proxy.git"
	Version   = "<unset>"
	Revision  = "<unset>"
	BuildDate = "<unset>"
)

func printSummary() {
	zlog.Info().Msgf("Starting Remote Access Proxy")
	zlog.InfraSec().Info().Msgf("RepoURL: %s, Version: %s, Revision: %s, BuildDate: %s\n", RepoURL, Version, Revision, BuildDate)
}

func setupTracing(traceURL string) func(context.Context) error {
	cleanup, exportErr := tracing.NewTraceExporterHTTP(traceURL, name, nil)
	if exportErr != nil {
		zlog.Err(exportErr).Msg("Error creating trace exporter")
	}
	if cleanup != nil {
		zlog.Info().Msgf("Tracing enabled %s", traceURL)
	} else {
		zlog.Info().Msg("Tracing disabled")
	}
	return cleanup
}

func setupOamServer(enableTracing bool, oamservaddr string) {
	if oamservaddr != "" {
		// Add oam grpc server
		wg.Add(1)
		go func() {
			if err := oam.StartOamGrpcServer(termChan, readyChan, &wg, oamservaddr, enableTracing); err != nil {
				zlog.InfraSec().Fatal().Err(err).Msg("Cannot start Remote Access Proxy OAM gRPC server")
			}
		}()
		// Don't signal ready here - wait until all services are actually running
		// readyChan <- true will be sent after all servers start (see main function)
	}
}

func main() {
	flag.Parse()
	// Print a summary of the build
	printSummary()

	// Internal-only Chisel user so the server requires auth (no open mode when user index is empty).
	// Dependency contract: internal/chiselauth.TestSpec_ChiselDependency_authWithBarrierUser
	chiselBarrierAuth, err := chiselauth.GenerateCredentials()
	if err != nil {
		zlog.InfraSec().Fatal().Err(err).Msg("Failed to generate Chisel barrier credentials")
	}
	barrierUser, _, _ := strings.Cut(chiselBarrierAuth, ":")
	zlog.InfraSec().Info().Str("chisel_barrier_user", barrierUser).Msg("Chisel: per-RAC users via reconcile; barrier user not for agents (password not logged)")

	// Load configuration from flags
	conf := config.RemoteAccessProxyConfig{
		InventoryAddr:           *inventoryAddress,
		OAMServerAddr:           *oamservaddr,
		EnableTracing:           *enableTracing,
		TraceURL:                *traceURL,
		EnableMetrics:           *enableMetrics,
		MetricsAddr:             *metricsAddress,
		InsecureGRPC:            *insecureGrpc,
		CACertPath:              *caCertPath,
		TLSKeyPath:              *tlsKeyPath,
		TLSCertPath:             *tlsCertPath,
		InventoryTimeout:        *inventoryTimeout,
		ListAllInventoryTimeout: *listAllInventoryTimeout,
		ReconcileTickerPeriod:   *reconcileTickerPeriod,
		ReconcileParallelism:    *reconcileParallelism,
		WebSocketAddr:           *wsAddr,
		ChiselBindAddr:          *chiselBindAddr,
		ChiselPort:              *chiselPort,
		ChiselKeySeed:           *chiselKeySeed,
		ChiselAuth:              chiselBarrierAuth,
		ChiselKeepAlive:         *chiselKeepAlive,
		ReverseSSHAddr:          *reverseSSHAddr,
		ReverseSSHWaitTimeout:   *reverseSSHWaitTimeout,
		EnableUUIDCache:         *invCacheUUIDEnable,
		UUIDCacheTTL:            *invCacheStaleTimeout,
		UUIDCacheTTLOffset:      int(*invCacheStaleTimeoutOffset),
	}

	if err := conf.Validate(); err != nil {
		zlog.InfraSec().Fatal().Err(err).Msg("Failed to start due to invalid configuration")
	}

	confForLog := conf
	confForLog.ChiselAuth = "<redacted>"
	zlog.Info().Msgf("Starting Remote Access Proxy conf %+v", confForLog)

	// Startup order, respecting deps:
	// 1. Setup tracing
	// 2. Start Inventory client
	// 3. Start NBHandler and the reconcilers
	// 4. Start Chisel server
	// 5. Start WebSocket terminal server
	// 6. Start the OAM server

	if conf.EnableTracing {
		cleanup := setupTracing(conf.TraceURL)
		if cleanup != nil {
			defer func() {
				err := cleanup(context.Background())
				if err != nil {
					zlog.Err(err).Msg("Error in tracing cleanup")
				}
			}()
		}
	}

	if conf.EnableMetrics {
		metrics.StartMetricsExporter([]prometheus.Collector{metrics.GetClientMetricsWithLatency()},
			metrics.WithListenAddress(conf.MetricsAddr))
	}

	chiselCfg := &chserver.Config{
		KeySeed:   conf.ChiselKeySeed,
		Auth:      conf.ChiselAuth,
		Reverse:   true,
		KeepAlive: conf.ChiselKeepAlive,
	}
	chisrv, err := chserver.NewServer(chiselCfg)
	if err != nil {
		zlog.Fatal().Err(err).Msg("Failed to create Chisel server")
	}
	chiselReg := reconcilers.NewChiselServerRegistrar(chisrv)

	// Connect to Inventory
	netClient, err := clients.NewRAInventoryClientWithOptions(
		clients.WithInventoryAddress(conf.InventoryAddr),
		clients.WithEnableTracing(conf.EnableTracing),
		clients.WithEnableMetrics(conf.EnableMetrics),
		clients.WithInsecureGRPC(conf.InsecureGRPC),
		clients.WithTLS(conf.CACertPath, conf.TLSCertPath, conf.TLSKeyPath),
		clients.WithInventoryTimeout(conf.InventoryTimeout),
		clients.WithListAllInventoryTimeout(conf.ListAllInventoryTimeout),
		clients.WithUUIDCache(conf.EnableUUIDCache, conf.UUIDCacheTTL, conf.UUIDCacheTTLOffset),
	)
	if err != nil {
		zlog.InfraSec().Fatal().Err(err).Msgf("Unable to start Remote Access Proxy Inventory client")
	}

	// Start Northbound Handler with reconcilers
	nbHandler, err := handlers.NewNBHandler(netClient, conf.EnableTracing, conf.ReconcileTickerPeriod, conf.ReconcileParallelism, conf.InventoryTimeout, conf.ListAllInventoryTimeout, chiselReg)
	if err != nil {
		zlog.InfraSec().Fatal().Err(err).Msgf("Unable to create Northbound Handler")
	}
	err = nbHandler.Start()
	if err != nil {
		zlog.InfraSec().Fatal().Err(err).Msgf("Unable to start Northbound Handler")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		zlog.Info().Msgf("RAP: Chisel listening on %s:%s", conf.ChiselBindAddr, conf.ChiselPort)
		if err := chisrv.StartContext(ctx, conf.ChiselBindAddr, conf.ChiselPort); err != nil {
			zlog.Err(err).Msg("Chisel server error")
		}
	}()

	// Start WebSocket terminal server
	mux := http.NewServeMux()
	mux.HandleFunc(
		"/term",
		wsterm.NewInventoryHandler(wsterm.InventoryHandlerConfig{
			HandlerConfig: wsterm.HandlerConfig{
				ReverseSSHAddr:        conf.ReverseSSHAddr,
				ReverseSSHWaitTimeout: conf.ReverseSSHWaitTimeout,
				SSHUser:               "vendev",
				PrivateKeyPath:        strings.TrimSpace(*sshPrivateKeyPath),
				Password:              strings.TrimSpace(*sshPassword),
			},
			NetClient:        netClient,
			InventoryTimeout: conf.InventoryTimeout,
		}),
	)

	wsSrv := &http.Server{
		Addr:              conf.WebSocketAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		zlog.Info().Msgf("RAP: WebSocket terminal on ws://%s/term", conf.WebSocketAddr)
		if err := wsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			zlog.Err(err).Msg("WebSocket server error")
		}
	}()

	setupOamServer(conf.EnableTracing, conf.OAMServerAddr)

	// Signal OAM server that we are ready after all services started
	// Wait a brief moment to ensure OAM server has started listening
	if conf.OAMServerAddr != "" {
		time.Sleep(100 * time.Millisecond) // Brief delay to ensure OAM server is listening
		readyChan <- true
	}

	// Graceful shutdown
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigChan
		zlog.InfraSec().Info().Msg("Received termination signal, shutting down...")
		close(termChan)
		// Stop Northbound Handler
		nbHandler.Stop()
		// Stop Inventory client
		netClient.Stop()
		// Stop Chisel server
		_ = chisrv.Close()
		// Stop WebSocket server
		_ = wsSrv.Shutdown(context.Background())
	}()

	wg.Wait()
	zlog.Info().Msg("Remote Access Proxy stopped")
}

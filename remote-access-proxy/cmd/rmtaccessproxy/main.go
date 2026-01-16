// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	chserver "github.com/jpillora/chisel/server"
	"github.com/prometheus/client_golang/prometheus"

	inv_client "github.com/open-edge-platform/infra-core/inventory/v2/pkg/client"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/logging"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/metrics"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/oam"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/tracing"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/clients"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/common"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/internal/handlers"
	"github.com/open-edge-platform/infra-managers/remote-access-proxy/pkg/config"
	"golang.org/x/crypto/ssh"
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
	chiselAuth              = flag.String(common.ChiselAuth, common.DefaultChiselAuth, common.ChiselAuthDescription)
	chiselKeepAlive         = flag.Duration(common.ChiselKeepAlive, common.DefaultChiselKeepAlive, common.ChiselKeepAliveDescription)
	reverseSSHAddr          = flag.String(common.ReverseSSHAddr, common.DefaultReverseSSHAddr, common.ReverseSSHAddrDescription)
	reverseSSHWaitTimeout   = flag.Duration(common.ReverseSSHWaitTimeout, common.DefaultReverseSSHWaitTimeout, common.ReverseSSHWaitTimeoutDescription)
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

// ---- WS message schema ----
type wsMsg struct {
	Type string `json:"type"`           // "stdio" | "resize"
	Data string `json:"data,omitempty"` // for stdio
	Cols int    `json:"cols,omitempty"` // for resize
	Rows int    `json:"rows,omitempty"` // for resize
}

// ---- SSH dialer to reverse port ----
func dialSSH(rows, cols int, term string, reverseSSHAddr string) (*ssh.Client, *ssh.Session, io.WriteCloser, io.Reader, error) {
	sshCfg := &ssh.ClientConfig{
		User:            "ubuntu",
		Auth:            []ssh.AuthMethod{ssh.Password("zaq12wsx")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	cli, err := ssh.Dial("tcp", reverseSSHAddr, sshCfg)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	sess, err := cli.NewSession()
	if err != nil {
		_ = cli.Close()
		return nil, nil, nil, nil, err
	}

	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}
	if term == "" {
		term = "xterm-256color"
	}

	if err := sess.RequestPty(term, rows, cols, ssh.TerminalModes{}); err != nil {
		_ = sess.Close()
		_ = cli.Close()
		return nil, nil, nil, nil, err
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		_ = cli.Close()
		return nil, nil, nil, nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		_ = cli.Close()
		return nil, nil, nil, nil, err
	}
	stderr, _ := sess.StderrPipe()
	reader := io.MultiReader(stdout, stderr)

	if err := sess.Shell(); err != nil {
		_ = sess.Close()
		_ = cli.Close()
		return nil, nil, nil, nil, err
	}

	return cli, sess, stdin, reader, nil
}

// ---- WS handler: /term ----
var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 * 1024,
	WriteBufferSize: 32 * 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func makeTermHandler(reverseSSHAddr string, reverseSSHWaitTimeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WS upgrade: %v", err)
			return
		}
		defer conn.Close()

		if err := waitPort(reverseSSHAddr, reverseSSHWaitTimeout); err != nil {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"stdio","data":"[RAP] reverse not ready\n"}`))
			return
		}

		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var initRows, initCols int
		var termName string
		{
			mt, payload, err := conn.ReadMessage()
			if err == nil && mt == websocket.TextMessage {
				var m wsMsg
				if json.Unmarshal(payload, &m) == nil && m.Type == "resize" {
					initRows, initCols = m.Rows, m.Cols
				}
				termName = r.URL.Query().Get("term")
			}
		}
		_ = conn.SetReadDeadline(time.Time{})

		sshClient, sess, stdin, stdout, err := dialSSH(initRows, initCols, termName, reverseSSHAddr)
		if err != nil {
			errMsg := fmt.Sprintf(`{"type":"stdio","data":"[RAP] ssh error: %s\n"}`, err.Error())
			_ = conn.WriteMessage(websocket.TextMessage, []byte(errMsg))
			return
		}
		defer func() { _ = sess.Close(); _ = sshClient.Close() }()

		conn.SetPongHandler(func(string) error { return nil })
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for range t.C {
				_ = conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(5*time.Second))
			}
		}()

		var writeMu sync.Mutex
		go func() {
			buf := make([]byte, 32*1024)
			for {
				n, err := stdout.Read(buf)
				if n > 0 {
					out := wsMsg{Type: "stdio", Data: string(buf[:n])}
					b, _ := json.Marshal(out)
					writeMu.Lock()
					_ = conn.WriteMessage(websocket.TextMessage, b)
					writeMu.Unlock()
				}
				if err != nil {
					writeMu.Lock()
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"stdio","data":"[RAP] stream closed\n"}`))
					writeMu.Unlock()
					return
				}
			}
		}()

		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				log.Printf("WS read: %v", err)
				return
			}
			var m wsMsg
			if err := json.Unmarshal(payload, &m); err != nil {
				continue
			}
			switch m.Type {
			case "stdio":
				if _, err := io.WriteString(stdin, m.Data); err != nil {
					return
				}
			case "resize":
				if m.Cols > 0 && m.Rows > 0 {
					_ = sess.WindowChange(m.Rows, m.Cols)
				}
			}
		}
	}
}

// ---- utils ----
func waitPort(addr string, max time.Duration) error {
	deadline := time.Now().Add(max)
	for {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("port not ready")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func main() {
	flag.Parse()
	// Print a summary of the build
	printSummary()

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
		ChiselAuth:              *chiselAuth,
		ChiselKeepAlive:         *chiselKeepAlive,
		ReverseSSHAddr:          *reverseSSHAddr,
		ReverseSSHWaitTimeout:   *reverseSSHWaitTimeout,
		EnableUUIDCache:         *invCacheUUIDEnable,
		UUIDCacheTTL:            *invCacheStaleTimeout,
		UUIDCacheTTLOffset:      int(*invCacheStaleTimeoutOffset),
	}

	if err := conf.Validate(); err != nil {
		zlog.InfraSec().Fatal().Err(err).Msgf("Failed to start due to invalid configuration: %v", conf)
	}

	zlog.Info().Msgf("Starting Remote Access Proxy conf %+v", conf)

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
	nbHandler, err := handlers.NewNBHandler(netClient, conf.EnableTracing, conf.ReconcileTickerPeriod, conf.ReconcileParallelism, conf.InventoryTimeout, conf.ListAllInventoryTimeout)
	if err != nil {
		zlog.InfraSec().Fatal().Err(err).Msgf("Unable to create Northbound Handler")
	}
	err = nbHandler.Start()
	if err != nil {
		zlog.InfraSec().Fatal().Err(err).Msgf("Unable to start Northbound Handler")
	}

	// Start Chisel server for reverse tunneling
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
	go func() {
		zlog.Info().Msgf("RAP: Chisel listening on %s:%s", conf.ChiselBindAddr, conf.ChiselPort)
		if err := chisrv.StartContext(ctx, conf.ChiselBindAddr, conf.ChiselPort); err != nil {
			zlog.Err(err).Msg("Chisel server error")
		}
	}()

	// Start WebSocket terminal server
	mux := http.NewServeMux()
	mux.HandleFunc("/term", makeTermHandler(conf.ReverseSSHAddr, conf.ReverseSSHWaitTimeout))

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

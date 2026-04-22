package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	inv_v1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/inventory/v1"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/client"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/util"
)

func main() {
	addr := flag.String("inventory", "127.0.0.1:50051", "Inventory gRPC address")
	tenant := flag.String("tenant", os.Getenv("TENANT_ID"), "Tenant UUID")
	flag.Parse()
	if *tenant == "" {
		log.Fatal("-tenant required")
	}
	if os.Getenv("HUMAN") == "" {
		_ = os.Setenv("HUMAN", "1")
	}
	wg := &sync.WaitGroup{}
	events := make(chan *client.WatchEvents, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cli, err := client.NewTenantAwareInventoryClient(ctx, client.InventoryClientConfig{
		Name:                      "rac-show-api",
		Address:                   *addr,
		AbortOnUnknownClientError: true,
		ClientKind:                inv_v1.ClientKind_CLIENT_KIND_API,
		ResourceKinds:             []inv_v1.ResourceKind{inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF},
		EnableTracing:             false,
		Wg:                        wg,
		Events:                    events,
		SecurityCfg:               &client.SecurityConfig{Insecure: true, CaPath: "", CertPath: "", KeyPath: ""},
	})
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer func() { _ = cli.Close() }()

	res, err := util.GetResourceFromKind(inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF)
	if err != nil {
		log.Fatalf("get res: %v", err)
	}
	listResp, err := cli.ListAll(ctx, &inv_v1.ResourceFilter{Resource: res, Limit: 100})
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	for _, r := range listResp {
		ra := r.GetRemoteAccess()
		if ra == nil {
			continue
		}
		if ra.GetTenantId() != *tenant {
			continue
		}
		tok := ra.GetSessionToken()
		if i := strings.IndexByte(tok, ':'); i > 0 {
			tok = tok[:i] + ":<redacted>"
		}
		fmt.Printf("RAC %s\n", ra.GetResourceId())
		fmt.Printf("  Instance:      %s\n", ra.GetInstance().GetResourceId())
		fmt.Printf("  User:          %q\n", ra.GetUser())
		fmt.Printf("  LocalPort:     %d\n", ra.GetLocalPort())
		fmt.Printf("  ProxyHost:     %s\n", ra.GetProxyHost())
		fmt.Printf("  SessionToken:  %s\n", tok)
		fmt.Printf("  DesiredState:  %s\n", ra.GetDesiredState())
		fmt.Printf("  CurrentState:  %s\n", ra.GetCurrentState())
		fmt.Printf("  Expiration:    %d (%s)\n", ra.GetExpirationTimestamp(), time.Unix(int64(ra.GetExpirationTimestamp()), 0).UTC())
		fmt.Printf("  ConfStatus:    %s\n", ra.GetConfigurationStatus())
		fmt.Println("")
	}
}

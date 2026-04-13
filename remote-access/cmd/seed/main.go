// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0
//
// Minimal reference for creating a RemoteAccessConfiguration in Inventory.
//
// gRPC: inventory.v1.InventoryService.CreateResource(CreateResourceRequest)
//   - client_uuid: assigned after SubscribeEvents (the inventory Go client fills this).
//   - tenant_id:   top-level UUID; must match remote_access.tenant_id in the nested resource.
//   - resource:    oneof — set resource.remote_access to remoteaccess.v1.RemoteAccessConfiguration.
//
// Nested RemoteAccessConfiguration (create):
//   - Do not set resource_id (server generates rmtacconf-[0-9a-f]{8}).
//   - tenant_id:            required UUID.
//   - instance.resource_id: required; must reference an existing Instance in that tenant.
//   - expiration_timestamp: required; Unix seconds; must be > now+10m and < now+24h (store rules).
//   - desired_state:        e.g. REMOTE_ACCESS_STATE_ENABLED.
//   - local_port, proxy_host, user, session_token, target_host/target_port: optional at create;
//     RAM / RMs typically fill binding fields when reconciling.
//
// See: infra-core/inventory/api/inventory/v1/inventory.proto (CreateResourceRequest)
//      infra-core/inventory/api/remoteaccess/v1/remoteaccess.proto
//      infra-core/inventory/internal/store/remoteaccess_validator.go
//      infra-core/inventory/internal/inventory/inventory.go (case *Resource_RemoteAccess)
//
// Removing RAC: Inventory DeleteResource only soft-deletes (desired_state=DELETED). This tool
// completes hard delete via UpdateResource (current_state=DELETED) as CLIENT_KIND_RESOURCE_MANAGER,
// matching internal/store/remoteaccess_test.go.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	computev1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/compute/v1"
	inv_v1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/inventory/v1"
	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/client"
	inv_errors "github.com/open-edge-platform/infra-core/inventory/v2/pkg/errors"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/util"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func main() {
	addr := flag.String("inventory", getenvDefault("INVENTORY_ADDR", "127.0.0.1:50051"), "Inventory gRPC address host:port")
	tenant := flag.String("tenant", os.Getenv("TENANT_ID"), "Tenant UUID")
	instance := flag.String("instance", os.Getenv("INSTANCE_RESOURCE_ID"), "Existing Instance resource_id (inst-…)")
	deleteID := flag.String("delete", os.Getenv("DELETE_RAC_ID"), "Optional: hard-delete this RemoteAccessConfiguration resource_id (e.g. rmtacconf-…); ignores NotFound")
	printInstForRAC := flag.String("print-instance-for-rac", os.Getenv("PRINT_INSTANCE_FOR_RAC"), "Optional: print Instance resource_id for this RAC (stdout only) and exit")
	cleanupHost := flag.String("cleanup-for-host", os.Getenv("CLEANUP_RAC_HOST_ID"), "Optional: delete all RAC for this Host resource_id in -tenant (fixes ambiguous count>1)")
	listTenant := flag.Bool("list", false, "List all RemoteAccessConfiguration resources for -tenant (count + resource_id) and exit")
	cleanupAllRAC := flag.Bool("cleanup-all-rac", false, "Hard-delete every RemoteAccessConfiguration in -tenant (dangerous)")
	validFor := flag.Duration("valid-for", 12*time.Hour, "Duration from now until expiration (must fall within server window >10m and <24h)")
	flag.Parse()

	if *tenant == "" {
		log.Fatal("required: -tenant (or env TENANT_ID)")
	}
	if !*listTenant && *instance == "" && *cleanupHost == "" && *deleteID == "" && !*cleanupAllRAC && *printInstForRAC == "" {
		log.Fatal("required: -instance (or env INSTANCE_RESOURCE_ID), or -cleanup-for-host, or -list, or -delete, or -cleanup-all-rac, or -print-instance-for-rac\n" +
			"Example: go run . -tenant <uuid> -instance inst-abc1234\n" +
			"Cleanup:  go run . -tenant <uuid> -cleanup-for-host host-xxxxxxxx\n" +
			"List:     go run . -tenant <uuid> -list\n" +
			"Hard-del: go run . -tenant <uuid> -delete rmtacconf-xxxxxxxx\n" +
			"All RAC:  go run . -tenant <uuid> -cleanup-all-rac\n" +
			"Inst id:  go run . -tenant <uuid> -print-instance-for-rac rmtacconf-xxxxxxxx")
	}

	ctx := context.Background()
	events := make(chan *client.WatchEvents, 10)
	wg := &sync.WaitGroup{}
	racKinds := []inv_v1.ResourceKind{inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF}

	apiCli, err := client.NewTenantAwareInventoryClient(ctx, client.InventoryClientConfig{
		Name:          "rmtaccess-seed-api",
		Address:       *addr,
		Events:        events,
		Wg:            wg,
		SecurityCfg:   &client.SecurityConfig{Insecure: true},
		ClientKind:    inv_v1.ClientKind_CLIENT_KIND_API,
		ResourceKinds: racKinds,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = apiCli.Close() }()

	if *printInstForRAC != "" {
		getResp, err := apiCli.Get(ctx, *tenant, *printInstForRAC)
		if err != nil {
			log.Fatal(err)
		}
		ra := getResp.GetResource().GetRemoteAccess()
		if ra == nil {
			log.Fatalf("get %s: not a remote_access resource", *printInstForRAC)
		}
		inst := ra.GetInstance()
		if inst == nil || inst.GetResourceId() == "" {
			log.Fatalf("RAC %s: missing instance.resource_id", *printInstForRAC)
		}
		fmt.Println(inst.GetResourceId())
		return
	}

	if *listTenant {
		if err := listAllRACForTenant(ctx, apiCli, *tenant); err != nil {
			log.Fatal(err)
		}
		return
	}

	var rmCli client.TenantAwareInventoryClient
	if *cleanupHost != "" || *deleteID != "" || *cleanupAllRAC {
		rmCli, err = client.NewTenantAwareInventoryClient(ctx, client.InventoryClientConfig{
			Name:          "rmtaccess-seed-rm",
			Address:       *addr,
			Events:        events,
			Wg:            wg,
			SecurityCfg:   &client.SecurityConfig{Insecure: true},
			ClientKind:    inv_v1.ClientKind_CLIENT_KIND_RESOURCE_MANAGER,
			ResourceKinds: racKinds,
		})
		if err != nil {
			log.Fatal(err)
		}
		defer func() { _ = rmCli.Close() }()
	}

	if *cleanupAllRAC {
		if err := deleteAllRACForTenant(ctx, apiCli, rmCli, *tenant); err != nil {
			log.Fatal(err)
		}
	}

	if *cleanupHost != "" {
		if err := deleteAllRACForHost(ctx, apiCli, rmCli, *tenant, *cleanupHost); err != nil {
			log.Fatal(err)
		}
	}

	if *deleteID != "" {
		if err := hardDeleteRAC(ctx, apiCli, rmCli, *tenant, *deleteID); err != nil {
			log.Fatalf("hard-delete %s: %v", *deleteID, err)
		}
		log.Printf("hard-deleted %s", *deleteID)
	}

	if *instance == "" {
		return
	}

	exp := time.Now().Add(*validFor)
	minExp := time.Now().Add(10*time.Minute + time.Second)
	maxExp := time.Now().Add(24*time.Hour - time.Second)
	if !exp.After(minExp) || !exp.Before(maxExp) {
		log.Fatalf("expiration must be strictly between 10m and 24h from now (--valid-for=%v)", *validFor)
	}

	// Same shape as infra-core/inventory/pkg/testing InvResourceDAO.createRemoteAccessConfiguration.
	rac := &remoteaccessv1.RemoteAccessConfiguration{
		TenantId:            *tenant,
		DesiredState:        remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ENABLED,
		ExpirationTimestamp: uint64(exp.Unix()),
		Instance:            &computev1.InstanceResource{ResourceId: *instance},
	}

	req := &inv_v1.Resource{
		Resource: &inv_v1.Resource_RemoteAccess{RemoteAccess: rac},
	}

	out, err := apiCli.Create(ctx, *tenant, req)
	if err != nil {
		log.Fatal(err)
	}

	id := out.GetRemoteAccess().GetResourceId()
	fmt.Println(id)
	_, _ = fmt.Fprintf(os.Stderr, "RemoteAccessConfiguration resource_id: %s\n", id)
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// deleteAllRACForHost hard-deletes every RemoteAccessConfiguration linked to the given Host resource_id
// in the tenant (same filter as RAM ResolveRemoteAccessConfiguration).
func deleteAllRACForHost(ctx context.Context, apiCli, rmCli client.TenantAwareInventoryClient, tenantID, hostResourceID string) error {
	filterStr := fmt.Sprintf("%s.%s.%s = %q AND %s = %q",
		remoteaccessv1.RemoteAccessConfigurationEdgeInstance,
		computev1.InstanceResourceEdgeHost,
		computev1.HostResourceFieldResourceId,
		hostResourceID,
		remoteaccessv1.RemoteAccessConfigurationFieldTenantId,
		tenantID,
	)
	res, err := util.GetResourceFromKind(inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF)
	if err != nil {
		return err
	}
	const limit = 50
	total := 0
	for {
		listResp, err := apiCli.List(ctx, &inv_v1.ResourceFilter{
			Filter:   filterStr,
			Resource: res,
			Limit:    limit,
			Offset:   0,
		})
		if err != nil {
			return err
		}
		resources := listResp.GetResources()
		if len(resources) == 0 {
			break
		}
		for _, row := range resources {
			id := row.GetResource().GetRemoteAccess().GetResourceId()
			if id == "" {
				continue
			}
			if err := hardDeleteRAC(ctx, apiCli, rmCli, tenantID, id); err != nil {
				return err
			}
			log.Printf("cleanup: hard-deleted %s", id)
			total++
		}
	}
	log.Printf("cleanup-for-host %s: hard-removed %d RAC(s)", hostResourceID, total)
	return nil
}

// hardDeleteRAC performs Inventory two-phase RAC removal: soft delete (API) then
// UpdateResource with current_state=DELETED (resource manager), which removes the row.
func hardDeleteRAC(ctx context.Context, apiCli, rmCli client.TenantAwareInventoryClient, tenantID, racID string) error {
	getResp, err := apiCli.Get(ctx, tenantID, racID)
	if inv_errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	ra := getResp.GetResource().GetRemoteAccess()
	if ra == nil {
		return fmt.Errorf("get %s: not a remote_access resource", racID)
	}
	if ra.GetDesiredState() != remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DELETED {
		_, err = apiCli.Delete(ctx, tenantID, racID)
		if inv_errors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("soft delete %s: %w", racID, err)
		}
		getResp, err = apiCli.Get(ctx, tenantID, racID)
		if inv_errors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		ra = getResp.GetResource().GetRemoteAccess()
		if ra == nil {
			return fmt.Errorf("get %s after soft delete: missing remote_access", racID)
		}
	}
	inst := ra.GetInstance()
	if inst == nil || inst.GetResourceId() == "" {
		return fmt.Errorf("RAC %s: instance edge required for hard delete", racID)
	}
	fm := &fieldmaskpb.FieldMask{
		Paths: []string{remoteaccessv1.RemoteAccessConfigurationFieldCurrentState},
	}
	_, err = rmCli.Update(ctx, tenantID, racID, fm, &inv_v1.Resource{
		Resource: &inv_v1.Resource_RemoteAccess{
			RemoteAccess: &remoteaccessv1.RemoteAccessConfiguration{
				CurrentState: remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DELETED,
				Instance:     &computev1.InstanceResource{ResourceId: inst.GetResourceId()},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("rm update (hard delete) %s: %w", racID, err)
	}
	return nil
}

// deleteAllRACForTenant hard-deletes every RemoteAccessConfiguration with the given tenant_id (paginated List).
func deleteAllRACForTenant(ctx context.Context, apiCli, rmCli client.TenantAwareInventoryClient, tenantID string) error {
	filterStr := fmt.Sprintf("%s = %q",
		remoteaccessv1.RemoteAccessConfigurationFieldTenantId,
		tenantID,
	)
	res, err := util.GetResourceFromKind(inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF)
	if err != nil {
		return err
	}
	const limit = 100
	total := 0
	for {
		// Always offset 0: deleting shifts remaining rows; advancing offset would skip some.
		listResp, err := apiCli.List(ctx, &inv_v1.ResourceFilter{
			Filter:   filterStr,
			Resource: res,
			Limit:    limit,
			Offset:   0,
		})
		if err != nil {
			return err
		}
		resources := listResp.GetResources()
		if len(resources) == 0 {
			break
		}
		for _, row := range resources {
			id := row.GetResource().GetRemoteAccess().GetResourceId()
			if id == "" {
				continue
			}
			if err := hardDeleteRAC(ctx, apiCli, rmCli, tenantID, id); err != nil {
				return err
			}
			log.Printf("cleanup-all-rac: hard-deleted %s", id)
			total++
		}
	}
	log.Printf("cleanup-all-rac: hard-removed %d RAC(s) in tenant %s", total, tenantID)
	return nil
}

// listAllRACForTenant prints every RemoteAccessConfiguration in the tenant (paginated List).
func listAllRACForTenant(ctx context.Context, invCli client.TenantAwareInventoryClient, tenantID string) error {
	filterStr := fmt.Sprintf("%s = %q",
		remoteaccessv1.RemoteAccessConfigurationFieldTenantId,
		tenantID,
	)
	res, err := util.GetResourceFromKind(inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF)
	if err != nil {
		return err
	}
	const limit = 100
	offset := uint32(0)
	var ids []string
	var totalReported int32
	for {
		listResp, err := invCli.List(ctx, &inv_v1.ResourceFilter{
			Filter:   filterStr,
			Resource: res,
			Limit:    limit,
			Offset:   offset,
		})
		if err != nil {
			return err
		}
		if offset == 0 {
			totalReported = listResp.GetTotalElements()
		}
		resources := listResp.GetResources()
		for _, row := range resources {
			id := row.GetResource().GetRemoteAccess().GetResourceId()
			if id != "" {
				ids = append(ids, id)
			}
		}
		if len(resources) < limit {
			break
		}
		offset += limit
	}
	fmt.Printf("total_elements (server): %d\n", totalReported)
	fmt.Printf("fetched rows: %d\n", len(ids))
	for _, id := range ids {
		fmt.Println(id)
	}
	return nil
}

// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package clients

import (
	"context"
	"fmt"
	"sync"
	"time"

	computev1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/compute/v1"
	inv_v1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/inventory/v1"
	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/client"
	inv_errors "github.com/open-edge-platform/infra-core/inventory/v2/pkg/errors"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/logging"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/util"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/validator"
	"github.com/open-edge-platform/infra-managers/remote-access/internal/utils"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

const (
	DefaultInventoryTimeout = 5 * time.Second
	batchSize               = 20

	// TODO: fine tune this longer timeout based on target scale and inventory client batch size.
	ListAllDefaultTimeout = time.Minute // Longer timeout for reconciling all resources
	// eventsWatcherBufSize is the buffer size for the events channel.
	eventsWatcherBufSize = 10
)

var (
	clientName = "RmtAccessInventoryClient"
	zlog       = logging.GetLogger(clientName)
)

type RmtAccessInventoryClient struct {
	Client  client.TenantAwareInventoryClient
	Watcher chan *client.WatchEvents
}

// Options is options for init of the Inventory client.
type Options struct {
	InventoryAddress        string
	EnableTracing           bool
	EnableMetrics           bool
	InsecureGRPC            bool
	CACertPath              string
	TLSKeyPath              string
	TLSCertPath             string
	InventoryTimeout        time.Duration
	ListAllInventoryTimeout time.Duration
	EnableUUIDCache         bool
	UUIDCacheTTL            time.Duration
	UUIDCacheTTLOffset      int
}

// Option is an Inventory client option.
type Option func(*Options)

// WithInventoryAddress sets the Inventory Address.
func WithInventoryAddress(invAddr string) Option {
	return func(options *Options) {
		options.InventoryAddress = invAddr
	}
}

// WithEnableTracing enable tracing.
func WithEnableTracing(enableTracing bool) Option {
	return func(options *Options) {
		options.EnableTracing = enableTracing
	}
}

func WithEnableMetrics(enableMetrics bool) Option {
	return func(options *Options) {
		options.EnableMetrics = enableMetrics
	}
}

// WithInsecureGRPC sets insecure GRPC mode.
func WithInsecureGRPC(insecure bool) Option {
	return func(options *Options) {
		options.InsecureGRPC = insecure
	}
}

// WithTLS sets TLS certificates paths.
func WithTLS(caCertPath, tlsCertPath, tlsKeyPath string) Option {
	return func(options *Options) {
		options.CACertPath = caCertPath
		options.TLSCertPath = tlsCertPath
		options.TLSKeyPath = tlsKeyPath
	}
}

// WithInventoryTimeout sets inventory timeout.
func WithInventoryTimeout(timeout time.Duration) Option {
	return func(options *Options) {
		options.InventoryTimeout = timeout
	}
}

// WithListAllInventoryTimeout sets list all inventory timeout.
func WithListAllInventoryTimeout(timeout time.Duration) Option {
	return func(options *Options) {
		options.ListAllInventoryTimeout = timeout
	}
}

// WithUUIDCache enables UUID cache with specified TTL and offset.
func WithUUIDCache(enable bool, ttl time.Duration, offset int) Option {
	return func(options *Options) {
		options.EnableUUIDCache = enable
		options.UUIDCacheTTL = ttl
		options.UUIDCacheTTLOffset = offset
	}
}

// Set default timeouts if not provided
func (o *Options) setDefaults() {
	if o.InventoryTimeout == 0 {
		o.InventoryTimeout = DefaultInventoryTimeout
	}
	if o.ListAllInventoryTimeout == 0 {
		o.ListAllInventoryTimeout = ListAllDefaultTimeout
	}
	if !o.InsecureGRPC && o.CACertPath == "" && o.TLSCertPath == "" && o.TLSKeyPath == "" {
		// Default to insecure if no TLS config provided
		o.InsecureGRPC = true
	}
}

// WithOptions sets the Inventory client options.
func WithOptions(options Options) Option {
	return func(opts *Options) {
		*opts = options
	}
}

// NewRAInventoryClientWithOptions creates a client by instantiating a new Inventory client. To be used in production.
func NewRAInventoryClientWithOptions(opts ...Option) (*RmtAccessInventoryClient, error) {
	// Misc preps for the client instantiation
	ctx := context.Background()
	var options Options
	for _, opt := range opts {
		opt(&options)
	}
	options.setDefaults()
	eventsWatcher := make(chan *client.WatchEvents, eventsWatcherBufSize)
	wg := sync.WaitGroup{}

	clientCfg := client.InventoryClientConfig{
		Name:                      clientName,
		Address:                   options.InventoryAddress,
		EnableRegisterRetry:       false,
		AbortOnUnknownClientError: true,
		SecurityCfg: &client.SecurityConfig{
			Insecure: options.InsecureGRPC,
			CaPath:   options.CACertPath,
			CertPath: options.TLSCertPath,
			KeyPath:  options.TLSKeyPath,
		},
		Events:     eventsWatcher,
		ClientKind: inv_v1.ClientKind_CLIENT_KIND_RESOURCE_MANAGER,
		ResourceKinds: []inv_v1.ResourceKind{
			inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF,
		},
		Wg:            &wg,
		EnableTracing: options.EnableTracing,
		EnableMetrics: options.EnableMetrics,
		ClientCache: client.InvClientCacheConfig{
			EnableUUIDCache: options.EnableUUIDCache,
			StaleTime:       options.UUIDCacheTTL,
			StateTimeOffset: options.UUIDCacheTTLOffset,
		},
	}
	invClient, err := client.NewTenantAwareInventoryClient(ctx, clientCfg)
	if err != nil {
		return nil, err
	}
	zlog.InfraSec().Info().Msgf("Inventory client started")
	return NewRAInventoryClient(invClient, eventsWatcher)
}

// NewRAInventoryClient creates a client that wraps an existing Inventory client. Mainly for testing.
func NewRAInventoryClient(
	invClient client.TenantAwareInventoryClient,
	watcher chan *client.WatchEvents) (
	*RmtAccessInventoryClient, error,
) {
	rmtAccessCl := &RmtAccessInventoryClient{
		Client:  invClient,
		Watcher: watcher,
	}
	return rmtAccessCl, nil
}

// Stop stops the client.
func (n *RmtAccessInventoryClient) Stop() {
	if err := n.Client.Close(); err != nil {
		zlog.InfraSec().InfraErr(err).Msgf("")
	}
	zlog.InfraSec().Info().Msgf("Inventory client stopped")
}

func (n *RmtAccessInventoryClient) GetRemoteAccessConf(ctx context.Context, tenantID, resourceID string, timeout time.Duration) (
	*remoteaccessv1.RemoteAccessConfiguration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Get the resource and validate
	resp, err := n.Client.Get(ctx, tenantID, resourceID)
	if err != nil {
		zlog.InfraSec().InfraErr(err).Msgf("Unable to get RmtAccessConfig: %s", utils.FormatTenantResourceID(tenantID, resourceID))
		return nil, err
	}
	remAccessConf := resp.GetResource().GetRemoteAccess()
	if err = validator.ValidateMessage(remAccessConf); err != nil {
		zlog.InfraSec().InfraErr(err).Msg("")
		return nil, inv_errors.Wrap(err)
	}
	return remAccessConf, nil
}

// ResolveRemoteAccessConfiguration loads the RemoteAccessConfiguration for an edge agent polling by
// host SMBIOS UUID (compute.v1.HostResource.uuid) within the tenant. Uses Inventory GetHostByUUID, then
// the RemoteAccessConfiguration linked to that host's instance (at most one; otherwise error).
// Returns (nil, nil) when the host or RAC does not exist — polling maps this to NONE.
// Direct fetch by RAC resource_id (rmtacconf-…) remains available via GetRemoteAccessConf for internal callers.
func (n *RmtAccessInventoryClient) ResolveRemoteAccessConfiguration(ctx context.Context, tenantID, hostUUID string, timeout time.Duration) (*remoteaccessv1.RemoteAccessConfiguration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	host, err := n.Client.GetHostByUUID(ctx, tenantID, hostUUID)
	if err != nil {
		if inv_errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	filterStr := fmt.Sprintf("%s.%s.%s = %q AND %s = %q",
		remoteaccessv1.RemoteAccessConfigurationEdgeInstance,
		computev1.InstanceResourceEdgeHost,
		computev1.HostResourceFieldResourceId,
		host.GetResourceId(),
		remoteaccessv1.RemoteAccessConfigurationFieldTenantId,
		tenantID,
	)
	res, err := util.GetResourceFromKind(inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF)
	if err != nil {
		return nil, err
	}
	listResp, err := n.Client.List(ctx, &inv_v1.ResourceFilter{
		Filter:   filterStr,
		Resource: res,
		Limit:    2,
		Offset:   0,
	})
	if err != nil {
		return nil, err
	}
	resources := listResp.GetResources()
	if len(resources) == 0 {
		return nil, nil
	}
	racID, ok := pickNewestRACResourceID(resources)
	if !ok {
		return nil, nil
	}
	if len(resources) > 1 {
		zlog.Warn().Msgf("multiple RemoteAccessConfiguration for host %s (count=%d); using newest by updated_at: %s",
			host.GetResourceId(), len(resources), racID)
	}
	return n.GetRemoteAccessConf(ctx, tenantID, racID, timeout)
}

// pickNewestRACResourceID chooses one resource_id when List returns several RAC rows for the same host
// (e.g. stale duplicates). Uses lexicographic RFC3339 on updated_at, then created_at.
func pickNewestRACResourceID(resources []*inv_v1.GetResourceResponse) (id string, ok bool) {
	var bestID, bestTS string
	for _, row := range resources {
		ra := row.GetResource().GetRemoteAccess()
		if ra == nil || ra.GetResourceId() == "" {
			continue
		}
		ts := ra.GetUpdatedAt()
		if ts == "" {
			ts = ra.GetCreatedAt()
		}
		if bestID == "" || ts > bestTS {
			bestID = ra.GetResourceId()
			bestTS = ts
		}
	}
	return bestID, bestID != ""
}

// UpdateRemoteAccessConfigState updates an existing  Remote Access Config desired and current state in Inventory.
func (n *RmtAccessInventoryClient) UpdateRemoteAccessConfigState(ctx context.Context, tenantID, resourceID string,
	remAccessConf *remoteaccessv1.RemoteAccessConfiguration, timeout time.Duration,
) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Omit updated_at from mask: Inventory ent hooks set it on update; sending it in the mask
	// led to clearEntMutate errors (updated_at / desired_state) and failed RAC state writes.
	// RAM owns current_state + configuration_status_indicator (§12.12 B); operational text is RAP-only.
	fieldMask := &fieldmaskpb.FieldMask{
		Paths: []string{
			remoteaccessv1.RemoteAccessConfigurationFieldCurrentState,
			remoteaccessv1.RemoteAccessConfigurationFieldConfigurationStatusIndicator,
		},
	}
	err := util.ValidateMaskAndFilterMessage(remAccessConf, fieldMask, true)
	if err != nil {
		return err
	}
	remAccessConf.ResourceId = resourceID
	resource := &inv_v1.Resource{
		Resource: &inv_v1.Resource_RemoteAccess{
			RemoteAccess: remAccessConf,
		},
	}
	_, err = n.Client.Update(ctx, tenantID, remAccessConf.GetResourceId(), fieldMask, resource)
	if err != nil {
		zlog.InfraSec().InfraErr(err).Msgf("Unable to update Remote Access Config tenantID=%s, resourceID=%s, UUID=%s",
			tenantID, remAccessConf.GetResourceId(), remAccessConf.GetInstance().GetHost().GetUuid())
		return err
	}
	return nil
}

// FindRemoteAccessConfigs finds existing Remote Access Configs in Inventory.
func (n *RmtAccessInventoryClient) FindRemoteAccessConfigs(ctx context.Context, timeout time.Duration) ([]*client.ResourceTenantIDCarrier, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := util.GetResourceFromKind(inv_v1.ResourceKind_RESOURCE_KIND_RMT_ACCESS_CONF)
	if err != nil {
		return nil, err
	}
	filter := &inv_v1.ResourceFilter{
		Resource: res,
	}
	rmtAccessCfgs, err := n.Client.FindAll(ctx, filter)
	if err != nil {
		zlog.InfraSec().InfraErr(err).Msgf("Unable to find all RemoteAccessConfigurations")
		return nil, err
	}
	return rmtAccessCfgs, nil
}

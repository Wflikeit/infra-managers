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
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

const (
	DefaultInventoryTimeout = 5 * time.Second

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
	// Defaults used when a method is called with timeout <= 0 (WithInventoryTimeout / WithListAllInventoryTimeout).
	defaultInventoryTimeout time.Duration
	defaultListAllTimeout   time.Duration
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

// WithOptions sets the Inventory client options.
func WithOptions(options Options) Option {
	return func(opts *Options) {
		*opts = options
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

// NewRAInventoryClientWithOptions creates a client by instantiating a new Inventory client. To be used in production.
// InventoryTimeout / ListAllInventoryTimeout from options are stored on the wrapper and used when RPC methods
// are called with timeout <= 0 (see inventoryCallTimeout / listAllCallTimeout); zero applies package defaults.
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
	return newRmtAccessInventoryClient(invClient, eventsWatcher, options.InventoryTimeout, options.ListAllInventoryTimeout)
}

func newRmtAccessInventoryClient(
	invClient client.TenantAwareInventoryClient,
	watcher chan *client.WatchEvents,
	inventoryTimeout, listAllTimeout time.Duration,
) (*RmtAccessInventoryClient, error) {
	if inventoryTimeout == 0 {
		inventoryTimeout = DefaultInventoryTimeout
	}
	if listAllTimeout == 0 {
		listAllTimeout = ListAllDefaultTimeout
	}
	return &RmtAccessInventoryClient{
		Client:                  invClient,
		Watcher:                 watcher,
		defaultInventoryTimeout: inventoryTimeout,
		defaultListAllTimeout:   listAllTimeout,
	}, nil
}

// NewRAInventoryClient creates a client that wraps an existing Inventory client. Mainly for testing.
func NewRAInventoryClient(
	invClient client.TenantAwareInventoryClient,
	watcher chan *client.WatchEvents,
) (*RmtAccessInventoryClient, error) {
	return newRmtAccessInventoryClient(invClient, watcher, DefaultInventoryTimeout, ListAllDefaultTimeout)
}

func (n *RmtAccessInventoryClient) inventoryCallTimeout(d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return n.defaultInventoryTimeout
}

func (n *RmtAccessInventoryClient) listAllCallTimeout(d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return n.defaultListAllTimeout
}

// Stop stops the client.
func (n *RmtAccessInventoryClient) Stop() {
	if err := n.Client.Close(); err != nil {
		zlog.InfraSec().InfraErr(err).Msgf("")
	}
	zlog.InfraSec().Info().Msgf("Inventory client stopped")
}

func formatTenantResourceID(tenantID, resourceID string) string {
	return fmt.Sprintf("[tenantID=%s, resourceID=%s]", tenantID, resourceID)
}

func (n *RmtAccessInventoryClient) GetRemoteAccessConf(
	ctx context.Context, tenantID, resourceID string, timeout time.Duration,
) (*remoteaccessv1.RemoteAccessConfiguration, error) {
	ctx, cancel := context.WithTimeout(ctx, n.inventoryCallTimeout(timeout))
	defer cancel()

	// Get the resource and validate
	resp, err := n.Client.Get(ctx, tenantID, resourceID)
	if err != nil {
		zlog.InfraSec().InfraErr(err).Msgf("Unable to get RmtAccessConfig: %s", formatTenantResourceID(tenantID, resourceID))
		return nil, err
	}
	remAccessConf := resp.GetResource().GetRemoteAccess()
	if err = validator.ValidateMessage(remAccessConf); err != nil {
		zlog.InfraSec().InfraErr(err).Msg("")
		return nil, inv_errors.Wrap(err)
	}
	return remAccessConf, nil
}

// ResolveRemoteAccessConfiguration loads a RemoteAccessConfiguration for the edge agent lookup key.
// Key is the Host SMBIOS UUID in the tenant (see compute.v1.HostResource.uuid).
// Returns (nil, nil) when no RAC exists yet or the host is unknown — for polling endpoints that map this to NONE.
func (n *RmtAccessInventoryClient) ResolveRemoteAccessConfiguration(
	ctx context.Context, tenantID, hostUUID string, timeout time.Duration,
) (*remoteaccessv1.RemoteAccessConfiguration, error) {
	ctx, cancel := context.WithTimeout(ctx, n.inventoryCallTimeout(timeout))
	defer cancel()

	host, err := n.Client.GetHostByUUID(ctx, tenantID, hostUUID)
	if err != nil {
		if inv_errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	inst := host.GetInstance()
	if inst == nil || inst.GetResourceId() == "" {
		return nil, nil
	}

	filterStr := fmt.Sprintf("%s.%s = %q AND %s = %q",
		remoteaccessv1.RemoteAccessConfigurationEdgeInstance,
		computev1.InstanceResourceFieldResourceId,
		inst.GetResourceId(),
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
	if len(resources) > 1 {
		return nil, fmt.Errorf(
			"ambiguous RemoteAccessConfiguration count=%d for host %s",
			len(resources), host.GetResourceId(),
		)
	}
	racID := resources[0].GetResource().GetRemoteAccess().GetResourceId()
	if racID == "" {
		return nil, nil
	}
	return n.GetRemoteAccessConf(ctx, tenantID, racID, timeout)
}

// UpdateRemoteAccessConfigState updates an existing Remote Access Config state in Inventory.
func (n *RmtAccessInventoryClient) UpdateRemoteAccessConfigState(
	ctx context.Context,
	tenantID, resourceID string,
	remAccessConf *remoteaccessv1.RemoteAccessConfiguration,
	timeout time.Duration,
) error {
	ctx, cancel := context.WithTimeout(ctx, n.inventoryCallTimeout(timeout))
	defer cancel()
	// Handcrafted PATCH update and validate before sending to Inventory.
	// Do not set or mask updated_at — Inventory rejects client writes to that field.
	// RAM owns current_state and configuration_status_indicator (+ timestamp).
	// configuration_status_code is owned by RAP (§12.12 B).
	fieldMask := &fieldmaskpb.FieldMask{
		Paths: []string{
			remoteaccessv1.RemoteAccessConfigurationFieldCurrentState,
			remoteaccessv1.RemoteAccessConfigurationFieldConfigurationStatusIndicator,
			remoteaccessv1.RemoteAccessConfigurationFieldConfigurationStatusTimestamp,
		},
	}
	if err := util.ValidateMaskAndFilterMessage(remAccessConf, fieldMask, true); err != nil {
		return err
	}
	remAccessConf.ResourceId = resourceID
	resource := &inv_v1.Resource{
		Resource: &inv_v1.Resource_RemoteAccess{
			RemoteAccess: remAccessConf,
		},
	}
	_, err := n.Client.Update(ctx, tenantID, resourceID, fieldMask, resource)
	if err != nil {
		zlog.InfraSec().InfraErr(err).Msgf(
			"Unable to update Remote Access Config state tenantID=%s, resourceID=%s",
			tenantID, resourceID,
		)
		return err
	}
	return nil
}

// SoftDeleteRemoteAccessConf sets desired_state=DELETED via Inventory DeleteResource.
func (n *RmtAccessInventoryClient) SoftDeleteRemoteAccessConf(
	ctx context.Context,
	tenantID, resourceID string,
	timeout time.Duration,
) error {
	ctx, cancel := context.WithTimeout(ctx, n.inventoryCallTimeout(timeout))
	defer cancel()

	_, err := n.Client.Delete(ctx, tenantID, resourceID)
	if inv_errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		zlog.InfraSec().InfraErr(err).Msgf(
			"Unable to soft-delete Remote Access Config tenantID=%s, resourceID=%s",
			tenantID, resourceID,
		)
	}
	return err
}

// FinalizeRemoteAccessConfDeletion completes RAC removal when desired_state is already DELETED.
func (n *RmtAccessInventoryClient) FinalizeRemoteAccessConfDeletion(
	ctx context.Context,
	tenantID, resourceID string,
	ra *remoteaccessv1.RemoteAccessConfiguration,
	timeout time.Duration,
) error {
	ctx, cancel := context.WithTimeout(ctx, n.inventoryCallTimeout(timeout))
	defer cancel()

	if ra == nil || ra.GetInstance() == nil || ra.GetInstance().GetResourceId() == "" {
		return fmt.Errorf("finalize RAC %s: instance edge required", resourceID)
	}

	fieldMask := &fieldmaskpb.FieldMask{
		Paths: []string{remoteaccessv1.RemoteAccessConfigurationFieldCurrentState},
	}
	patch := &remoteaccessv1.RemoteAccessConfiguration{
		ResourceId:   resourceID,
		CurrentState: remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DELETED,
		Instance:     ra.GetInstance(),
	}
	if err := util.ValidateMaskAndFilterMessage(patch, fieldMask, true); err != nil {
		return err
	}
	resource := &inv_v1.Resource{
		Resource: &inv_v1.Resource_RemoteAccess{
			RemoteAccess: patch,
		},
	}
	_, err := n.Client.Update(ctx, tenantID, resourceID, fieldMask, resource)
	if inv_errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		zlog.InfraSec().InfraErr(err).Msgf(
			"Unable to hard-delete Remote Access Config tenantID=%s, resourceID=%s",
			tenantID, resourceID,
		)
	}
	return err
}

// FindRemoteAccessConfigs finds existing Remote Access Configs in Inventory.
func (n *RmtAccessInventoryClient) FindRemoteAccessConfigs(
	ctx context.Context, timeout time.Duration,
) ([]*client.ResourceTenantIDCarrier, error) {
	ctx, cancel := context.WithTimeout(ctx, n.listAllCallTimeout(timeout))
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

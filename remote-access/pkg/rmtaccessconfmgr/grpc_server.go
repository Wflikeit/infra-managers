// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package rmtaccessconfmgr

import (
	"context"
	"time"

	remoteaccessv1 "github.com/open-edge-platform/infra-core/inventory/v2/pkg/api/remoteaccess/v1"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/tenant"
	pb "github.com/open-edge-platform/infra-managers/remote-access/pkg/api/rmtaccessmgr/v1"
	inv_client "github.com/open-edge-platform/infra-managers/remote-access/pkg/clients"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Server struct {
	inv              *inv_client.RmtAccessInventoryClient
	inventoryTimeout time.Duration
	pb.UnimplementedRmtaccessmgrServiceServer
}

func NewServer(inv *inv_client.RmtAccessInventoryClient, inventoryTimeout time.Duration) *Server {
	return &Server{inv: inv, inventoryTimeout: inventoryTimeout}
}

// GetRemoteAccessConfigByGuid: polling endpoint for agent.
// Inventory is source of truth.
func (s *Server) GetRemoteAccessConfigByGuid(
	ctx context.Context,
	req *pb.GetRemoteAccessConfigByGuidRequest,
) (*pb.GetResourceAccessConfigResponse, error) {

	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is nil")
	}
	if err := req.ValidateAll(); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	tenantID, present := tenant.GetTenantIDFromContext(ctx)
	if !present {
		return nil, status.Error(codes.Unauthenticated, "Tenant ID is not present in context")
	}
	uuid := req.GetUuid()

	// Inventory is source of truth; uuid is host SMBIOS UUID in the tenant.
	ra, err := s.inv.ResolveRemoteAccessConfiguration(ctx, tenantID, uuid, s.inventoryTimeout)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "inventory resolve remote access config: %v", err)
	}
	// Polling: no RAC / unknown host => NONE (not a gRPC error).
	if ra == nil {
		return &pb.GetResourceAccessConfigResponse{
			ObservedAt: timestamppb.Now(),
			Status:     pb.ConfigStatus_CONFIG_STATUS_NONE,
			Error:      nil,
		}, nil
	}

	now := time.Now().UTC()

	cfgStatus, spec, cfgErr := mapInventoryToAgentResponse(ra, now)

	resp := &pb.GetResourceAccessConfigResponse{
		Seq:        ra.GetConfigurationStatusTimestamp(), // best-effort "version"; can be 0 if unused
		ObservedAt: timestamppb.Now(),
		Status:     cfgStatus,
		Spec:       spec,
		Error:      cfgErr,
	}
	return resp, nil
}

func mapInventoryToAgentResponse(
	ra *remoteaccessv1.RemoteAccessConfiguration,
	now time.Time,
) (pb.ConfigStatus, *pb.AgentRemoteAccessSpec, *pb.ConfigError) {

	// If already marked ERROR by provider/manager
	if ra.GetCurrentState() == remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ERROR {
		return pb.ConfigStatus_CONFIG_STATUS_ERROR, nil, &pb.ConfigError{Code: "current_state=ERROR"}
	}

	// Desired DISABLED/DELETED => DISABLED (agent should stop / no-op)
	switch ra.GetDesiredState() {
	case remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DELETED,
		remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_DISABLED:
		return pb.ConfigStatus_CONFIG_STATUS_DISABLED, nil, nil
	case remoteaccessv1.RemoteAccessState_REMOTE_ACCESS_STATE_ERROR:
		return pb.ConfigStatus_CONFIG_STATUS_ERROR, nil, &pb.ConfigError{Code: "desired_state=ERROR"}
	}

	// Validate readiness (pending vs invalid)
	ready, code := evaluateReadiness(ra, now)
	switch ready {
	case readinessInvalid:
		return pb.ConfigStatus_CONFIG_STATUS_ERROR, nil, &pb.ConfigError{Code: code}
	case readinessPending:
		return pb.ConfigStatus_CONFIG_STATUS_PENDING, nil, nil
	case readinessReady:
		// fallthrough
	}

	spec := &pb.AgentRemoteAccessSpec{
		RemoteAccessProxyEndpoint: ra.GetProxyHost(), // should be agent-reachable RAP endpoint (ws/wss)
		SessionToken:              ra.GetSessionToken(),
		ReverseBindPort:           ra.GetLocalPort(), // RAP reverse bind port
		SshUser:                   ra.GetUser(),
		ExpirationTimestamp:       ra.GetExpirationTimestamp(),
		Uuid:                      ra.GetResourceId(), // ra id
	}

	return pb.ConfigStatus_CONFIG_STATUS_ACTIVE, spec, nil
}

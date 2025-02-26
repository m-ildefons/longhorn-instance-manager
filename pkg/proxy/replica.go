package proxy

import (
	"strings"

	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	grpccodes "google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	eclient "github.com/longhorn/longhorn-engine/pkg/controller/client"
	esync "github.com/longhorn/longhorn-engine/pkg/sync"
	eptypes "github.com/longhorn/longhorn-engine/proto/ptypes"
	spdkclient "github.com/longhorn/longhorn-spdk-engine/pkg/client"
	spdktypes "github.com/longhorn/longhorn-spdk-engine/pkg/types"

	rpc "github.com/longhorn/longhorn-instance-manager/pkg/imrpc"
)

func (p *Proxy) ReplicaAdd(ctx context.Context, req *rpc.EngineReplicaAddRequest) (resp *emptypb.Empty, err error) {
	log := logrus.WithFields(logrus.Fields{
		"serviceURL":     req.ProxyEngineRequest.Address,
		"engineName":     req.ProxyEngineRequest.EngineName,
		"volumeName":     req.ProxyEngineRequest.VolumeName,
		"replicaName":    req.ReplicaName,
		"replicaAddress": req.ReplicaAddress,
		"restore":        req.Restore,
		"size":           req.Size,
		"currentSize":    req.CurrentSize,
		"fastSync":       req.FastSync,
	})
	log.Info("Adding replica")

	switch req.ProxyEngineRequest.DataEngine {
	case rpc.DataEngine_DATA_ENGINE_V1:
		return p.replicaAdd(ctx, req)
	case rpc.DataEngine_DATA_ENGINE_V2:
		return p.spdkReplicaAdd(ctx, req)
	default:
		return nil, grpcstatus.Errorf(grpccodes.InvalidArgument, "unknown data engine %v", req.ProxyEngineRequest.DataEngine)
	}
}

func (p *Proxy) replicaAdd(ctx context.Context, req *rpc.EngineReplicaAddRequest) (resp *emptypb.Empty, err error) {
	task, err := esync.NewTask(ctx, req.ProxyEngineRequest.Address, req.ProxyEngineRequest.VolumeName,
		req.ProxyEngineRequest.EngineName)
	if err != nil {
		return nil, err
	}

	if req.Restore {
		if err := task.AddRestoreReplica(req.Size, req.CurrentSize, req.ReplicaAddress, req.ReplicaName); err != nil {
			return nil, err
		}
	} else {
		if err := task.AddReplica(req.Size, req.CurrentSize, req.ReplicaAddress, req.ReplicaName,
			int(req.FileSyncHttpClientTimeout), req.FastSync); err != nil {
			return nil, err
		}
	}
	return &emptypb.Empty{}, nil
}

func (p *Proxy) spdkReplicaAdd(ctx context.Context, req *rpc.EngineReplicaAddRequest) (resp *emptypb.Empty, err error) {
	c, err := spdkclient.NewSPDKClient(p.spdkServiceAddress)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	replicaAddress := strings.TrimPrefix(req.ReplicaAddress, "tcp://")

	err = c.EngineReplicaAdd(req.ProxyEngineRequest.EngineName, req.ReplicaName, replicaAddress)
	if err != nil {
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

func (p *Proxy) ReplicaList(ctx context.Context, req *rpc.ProxyEngineRequest) (resp *rpc.EngineReplicaListProxyResponse, err error) {
	log := logrus.WithFields(logrus.Fields{
		"serviceURL": req.Address,
		"engineName": req.EngineName,
		"volumeName": req.VolumeName,
		"dataEngine": req.DataEngine,
	})
	log.Trace("Listing replicas")

	switch req.DataEngine {
	case rpc.DataEngine_DATA_ENGINE_V1:
		return p.replicaList(ctx, req)
	case rpc.DataEngine_DATA_ENGINE_V2:
		return p.spdkReplicaList(ctx, req)
	default:
		return nil, grpcstatus.Errorf(grpccodes.InvalidArgument, "unknown data engine %v", req.DataEngine)
	}
}

func (p *Proxy) replicaList(ctx context.Context, req *rpc.ProxyEngineRequest) (resp *rpc.EngineReplicaListProxyResponse, err error) {
	c, err := eclient.NewControllerClient(req.Address, req.VolumeName, req.EngineName)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	recv, err := c.ReplicaList()
	if err != nil {
		return nil, err
	}

	replicas := []*eptypes.ControllerReplica{}
	for _, r := range recv {
		replica := &eptypes.ControllerReplica{
			Address: &eptypes.ReplicaAddress{
				Address: r.Address,
			},
			Mode: eptypes.ReplicaModeToGRPCReplicaMode(r.Mode),
		}
		replicas = append(replicas, replica)
	}

	return &rpc.EngineReplicaListProxyResponse{
		ReplicaList: &eptypes.ReplicaListReply{
			Replicas: replicas,
		},
	}, nil
}

func replicaModeToGRPCReplicaMode(mode spdktypes.Mode) eptypes.ReplicaMode {
	switch mode {
	case spdktypes.ModeWO:
		return eptypes.ReplicaMode_WO
	case spdktypes.ModeRW:
		return eptypes.ReplicaMode_RW
	case spdktypes.ModeERR:
		return eptypes.ReplicaMode_ERR
	}
	return eptypes.ReplicaMode_ERR
}

func (p *Proxy) spdkReplicaList(ctx context.Context, req *rpc.ProxyEngineRequest) (resp *rpc.EngineReplicaListProxyResponse, err error) {
	c, err := spdkclient.NewSPDKClient(p.spdkServiceAddress)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	recv, err := c.EngineGet(req.EngineName)
	if err != nil {
		return nil, err
	}

	replicas := []*eptypes.ControllerReplica{}
	for replicaName, mode := range recv.ReplicaModeMap {
		address, ok := recv.ReplicaAddressMap[replicaName]
		if !ok {
			return nil, grpcstatus.Errorf(grpccodes.Internal, "failed to get replica address for %v", replicaName)
		}
		replica := &eptypes.ControllerReplica{
			Address: &eptypes.ReplicaAddress{
				Address: address,
			},
			Mode: replicaModeToGRPCReplicaMode(mode),
		}
		replicas = append(replicas, replica)
	}

	return &rpc.EngineReplicaListProxyResponse{
		ReplicaList: &eptypes.ReplicaListReply{
			Replicas: replicas,
		},
	}, nil
}

func (p *Proxy) ReplicaRebuildingStatus(ctx context.Context, req *rpc.ProxyEngineRequest) (resp *rpc.EngineReplicaRebuildStatusProxyResponse, err error) {
	log := logrus.WithFields(logrus.Fields{
		"serviceURL": req.Address,
		"engineName": req.EngineName,
		"volumeName": req.VolumeName,
		"dataEngine": req.DataEngine,
	})
	log.Trace("Getting replica rebuilding status")

	switch req.DataEngine {
	case rpc.DataEngine_DATA_ENGINE_V1:
		return p.replicaRebuildingStatus(ctx, req)
	case rpc.DataEngine_DATA_ENGINE_V2:
		return p.spdkReplicaRebuildingStatus(ctx, req)
	default:
		return nil, grpcstatus.Errorf(grpccodes.InvalidArgument, "unknown data engine %v", req.DataEngine)
	}
}

func (p *Proxy) replicaRebuildingStatus(ctx context.Context, req *rpc.ProxyEngineRequest) (resp *rpc.EngineReplicaRebuildStatusProxyResponse, err error) {
	task, err := esync.NewTask(ctx, req.Address, req.VolumeName, req.EngineName)
	if err != nil {
		return nil, err
	}

	recv, err := task.RebuildStatus()
	if err != nil {
		return nil, err
	}

	resp = &rpc.EngineReplicaRebuildStatusProxyResponse{
		Status: make(map[string]*eptypes.ReplicaRebuildStatusResponse),
	}
	for k, v := range recv {
		resp.Status[k] = &eptypes.ReplicaRebuildStatusResponse{
			Error:              v.Error,
			IsRebuilding:       v.IsRebuilding,
			Progress:           int32(v.Progress),
			State:              v.State,
			FromReplicaAddress: v.FromReplicaAddress,
		}
	}

	return resp, nil
}

func (p *Proxy) spdkReplicaRebuildingStatus(ctx context.Context, req *rpc.ProxyEngineRequest) (resp *rpc.EngineReplicaRebuildStatusProxyResponse, err error) {
	/* TODO: implement this */
	return &rpc.EngineReplicaRebuildStatusProxyResponse{
		Status: make(map[string]*eptypes.ReplicaRebuildStatusResponse),
	}, nil
}

func (p *Proxy) ReplicaVerifyRebuild(ctx context.Context, req *rpc.EngineReplicaVerifyRebuildRequest) (resp *emptypb.Empty, err error) {
	log := logrus.WithFields(logrus.Fields{"serviceURL": req.ProxyEngineRequest.Address})
	log.Infof("Verifying replica %v rebuild", req.ReplicaAddress)

	task, err := esync.NewTask(ctx, req.ProxyEngineRequest.Address, req.ProxyEngineRequest.VolumeName,
		req.ProxyEngineRequest.EngineName)
	if err != nil {
		return nil, err
	}

	err = task.VerifyRebuildReplica(req.ReplicaAddress, req.ReplicaName)
	if err != nil {
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

func (p *Proxy) ReplicaRemove(ctx context.Context, req *rpc.EngineReplicaRemoveRequest) (resp *emptypb.Empty, err error) {
	log := logrus.WithFields(logrus.Fields{
		"serviceURL":     req.ProxyEngineRequest.Address,
		"engineName":     req.ProxyEngineRequest.EngineName,
		"volumeName":     req.ProxyEngineRequest.VolumeName,
		"replicaName":    req.ReplicaName,
		"replicaAddress": req.ReplicaAddress,
	})
	log.Info("Removing replica")

	switch req.ProxyEngineRequest.DataEngine {
	case rpc.DataEngine_DATA_ENGINE_V1:
		if err := p.replicaDelete(ctx, req); err != nil {
			return nil, err
		}
	case rpc.DataEngine_DATA_ENGINE_V2:
		if err := p.spdkReplicaDelete(ctx, req); err != nil {
			return nil, err
		}
	default:
		return nil, grpcstatus.Errorf(grpccodes.InvalidArgument, "unknown data engine %v", req.ProxyEngineRequest.DataEngine)
	}

	return &emptypb.Empty{}, nil
}

func (p *Proxy) replicaDelete(ctx context.Context, req *rpc.EngineReplicaRemoveRequest) error {
	c, err := eclient.NewControllerClient(req.ProxyEngineRequest.Address, req.ProxyEngineRequest.VolumeName,
		req.ProxyEngineRequest.EngineName)
	if err != nil {
		return err
	}
	defer c.Close()

	return c.ReplicaDelete(req.ReplicaAddress)
}

func (p *Proxy) spdkReplicaDelete(ctx context.Context, req *rpc.EngineReplicaRemoveRequest) error {
	c, err := spdkclient.NewSPDKClient(p.spdkServiceAddress)
	if err != nil {
		return err
	}
	defer c.Close()

	return c.EngineReplicaDelete(req.ProxyEngineRequest.EngineName, req.ReplicaName, req.ReplicaAddress)
}

func (p *Proxy) ReplicaModeUpdate(ctx context.Context, req *rpc.EngineReplicaModeUpdateRequest) (resp *emptypb.Empty, err error) {
	log := logrus.WithFields(logrus.Fields{"serviceURL": req.ProxyEngineRequest.Address})
	log.Infof("Updating replica mode to %v", req.Mode)

	c, err := eclient.NewControllerClient(req.ProxyEngineRequest.Address, req.ProxyEngineRequest.VolumeName,
		req.ProxyEngineRequest.EngineName)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	if _, err = c.ReplicaUpdate(req.ReplicaAddress, eptypes.GRPCReplicaModeToReplicaMode(req.Mode)); err != nil {
		return nil, err
	}

	return &emptypb.Empty{}, nil
}

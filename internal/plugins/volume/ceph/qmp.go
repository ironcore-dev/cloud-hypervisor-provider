package ceph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/digitalocean/go-qemu/qmp"
	"github.com/go-logr/logr"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"os"
	"path/filepath"
	"strings"
)

type QMP struct {
	log     logr.Logger
	paths   host.Paths
	monitor *qmp.SocketMonitor
}

type BlockdevAddArguments struct {
	NodeName string `json:"node-name"`
	Driver   string `json:"driver"`
	Pool     string `json:"pool"`
	Image    string `json:"image"`
	User     string `json:"user"`
	Conf     string `json:"conf"`
	Discard  string `json:"discard"`
	Cache    struct {
		Direct bool `json:"direct"`
	} `json:"cache"`
}

type BlockExportAddArguments struct {
	ID       string `json:"id"`
	NodeName string `json:"node-name"`
	Type     string `json:"type"`
	Addr     struct {
		Type string `json:"type"`
		Path string `json:"path"`
	} `json:"addr"`
	Writable bool `json:"writable"`
}

type DeleteArguments struct {
	ID string `json:"id"`
}

type QMPRequest[T any] struct {
	Execute   string `json:"execute"`
	Arguments T      `json:"arguments,omitempty"`
}

var (
	ErrNotFound = errors.New("not found")
)

func (q *QMP) queryBlockNode(nodeName string) (*BlockDevice, error) {
	cmd, err := json.Marshal(QMPRequest[any]{
		Execute: "query-named-block-nodes",
	})
	if err != nil {
		return nil, fmt.Errorf("error marshalling cmd: %w", err)
	}

	res, err := q.monitor.Run(cmd)
	if err != nil {
		return nil, fmt.Errorf("error executing cmd: %w", err)
	}

	var devs BlockDevicesResponse
	if err := json.Unmarshal(res, &devs); err != nil {
		return nil, fmt.Errorf("error unmarshalling response: %w", err)
	}

	for _, dev := range devs.Data {
		if dev.NodeName == nodeName {
			return &dev, nil
		}
	}
	return nil, ErrNotFound
}

func (q *QMP) queryBlockExports(nodeName string) (*BlockExportNode, error) {
	cmd, err := json.Marshal(QMPRequest[any]{
		Execute: "query-block-exports",
	})
	if err != nil {
		return nil, fmt.Errorf("error marshalling cmd: %w", err)
	}

	res, err := q.monitor.Run(cmd)
	if err != nil {
		return nil, fmt.Errorf("error executing cmd: %w", err)
	}

	var devs BlockExportResponse
	if err := json.Unmarshal(res, &devs); err != nil {
		return nil, fmt.Errorf("error unmarshalling response: %w", err)
	}

	for _, dev := range devs.Data {
		if dev.ID == nodeName {
			return &dev, nil
		}
	}
	return nil, ErrNotFound
}

func (q *QMP) addBlockDev(volume *validatedVolume, confPath string) error {
	cmd, err := json.Marshal(QMPRequest[BlockdevAddArguments]{
		Execute: "blockdev-add",
		Arguments: BlockdevAddArguments{
			NodeName: fmt.Sprintf("ceph-%s", volume.handle),
			Driver:   "rbd",
			Pool:     volume.pool,
			Image:    volume.image,
			User:     volume.userID,
			Conf:     confPath,
			Discard:  "unmap",
			Cache: struct {
				Direct bool `json:"direct"`
			}{Direct: true},
		},
	})
	if err != nil {
		return fmt.Errorf("error marshalling cmd: %w", err)
	}

	if _, err := q.monitor.Run(cmd); err != nil {
		return fmt.Errorf("error executing cmd: %w", err)
	}

	return nil
}

func (q *QMP) deleteBlockDev(handle string) error {
	cmd, err := json.Marshal(QMPRequest[DeleteArguments]{
		Execute: "blockdev-add",
		Arguments: DeleteArguments{
			ID: handle,
		},
	})
	if err != nil {
		return fmt.Errorf("error marshalling cmd: %w", err)
	}

	if _, err := q.monitor.Run(cmd); err != nil {
		return fmt.Errorf("error executing cmd: %w", err)
	}

	return nil
}

func (q *QMP) exportBlockDev(handle string, socketPath string) error {
	cmd, err := json.Marshal(QMPRequest[BlockExportAddArguments]{
		Execute: "block-export-add",
		Arguments: BlockExportAddArguments{
			ID:       handle,
			NodeName: handle,
			Type:     "vhost-user-blk",
			Addr: struct {
				Type string `json:"type"`
				Path string `json:"path"`
			}{
				Type: "unix",
				Path: socketPath,
			},
			Writable: true,
		},
	})
	if err != nil {
		return fmt.Errorf("error marshalling cmd: %w", err)
	}

	if _, err := q.monitor.Run(cmd); err != nil {
		return fmt.Errorf("error executing cmd: %w", err)
	}

	return nil
}

func (q *QMP) deleteExportBlockDev(nodeName string) error {
	cmd, err := json.Marshal(QMPRequest[DeleteArguments]{
		Execute: "block-export-del",
		Arguments: DeleteArguments{
			ID: nodeName,
		},
	})
	if err != nil {
		return fmt.Errorf("error marshalling cmd: %w", err)
	}

	res, err := q.monitor.Run(cmd)
	if err != nil {
		return fmt.Errorf("error executing cmd: %w", err)
	}

	_ = res
	return ErrNotFound
}

func (q *QMP) Mount(ctx context.Context, machineID string, volume *validatedVolume) (string, error) {
	volumeDir := q.volumeDir(machineID, volume.handle)
	if err := os.MkdirAll(volumeDir, os.ModePerm); err != nil {
		return "", err
	}

	log := q.log.WithValues("machineID", machineID, "volumeID", volume.handle)
	socketPath := filepath.Join("/test", "socket")

	log.V(2).Info("Checking ceph conf")
	confPath, err := q.createCephConf(log, machineID, volume)
	if err != nil {
		return "", fmt.Errorf("error creating ceph conf: %w", err)
	}

	handle := fmt.Sprintf("ceph-%s", volume.handle)

	if _, err := q.queryBlockNode(handle); err != nil {
		if !errors.Is(err, ErrNotFound) {
			return "", fmt.Errorf("error querying block device: %w", err)
		}

		if err := q.addBlockDev(volume, confPath); err != nil {
			return "", fmt.Errorf("error adding block device: %w", err)
		}
	}

	if _, err := q.queryBlockExports(handle); err != nil {
		if !errors.Is(err, ErrNotFound) {
			return "", fmt.Errorf("error querying block device: %w", err)
		}

		if err := q.exportBlockDev(handle, socketPath); err != nil {
			return "", fmt.Errorf("error adding block device: %w", err)
		}
	}

	return socketPath, nil
}

func (q *QMP) createCephConf(log logr.Logger, machineID string, volume *validatedVolume) (string, error) {
	confPath := filepath.Join(
		q.volumeDir(machineID, volume.handle),
		"ceph.conf",
	)
	keyPath := filepath.Join(
		q.volumeDir(machineID, volume.handle),
		"ceph.key",
	)

	log.V(2).Info("Creating ceph conf", "confPath", confPath)
	confFile, err := os.OpenFile(confPath, os.O_CREATE|os.O_WRONLY, os.ModePerm)
	if err != nil {
		return "", fmt.Errorf("error opening conf file %s: %w", confPath, err)
	}

	confData := fmt.Sprintf(
		"[global]\nmon_host = %s \n\n[client.%s]\nkeyring = %s\n",
		strings.Join(volume.monitors, ","),
		volume.userID,
		keyPath,
	)
	_, err = confFile.WriteString(confData)
	if err != nil {
		return "", fmt.Errorf("error writing to conf file %s: %w", confPath, err)
	}

	log.V(1).Info("Creating ceph key", "keyPath", keyPath)
	keyFile, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY, os.ModePerm)
	if err != nil {
		return "", fmt.Errorf("error opening key file %s: %w", keyPath, err)
	}

	keyData := fmt.Sprintf("[client.%s]\nkey = %s\n", volume.userID, volume.userKey)
	_, err = keyFile.WriteString(keyData)
	if err != nil {
		return "", fmt.Errorf("error writing to key file %s: %w", keyPath, err)
	}

	return confPath, nil
}

func (q *QMP) Unmount(ctx context.Context, machineID string, volumeID string) error {

	handle := fmt.Sprintf("ceph-%s", volumeID)

	if _, err := q.queryBlockExports(handle); err != nil {
		if !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("error querying block device: %w", err)
		}

		if err := q.deleteExportBlockDev(handle); err != nil {
			return fmt.Errorf("error adding block device: %w", err)
		}
	}

	if _, err := q.queryBlockNode(handle); err != nil {
		if !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("error querying block device: %w", err)
		}

		if err := q.deleteBlockDev(handle); err != nil {
			return fmt.Errorf("error adding block device: %w", err)
		}
	}

	return nil

}

func (q *QMP) volumeDir(machineID string, volumeHandle string) string {
	return q.paths.MachineVolumeDir(machineID, cephDriverName, volumeHandle)
}

// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	goflag "flag"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/controllers"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/host"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/oci"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/networkinterface/options"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/volume"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/plugins/volume/ceph"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/raw"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/server"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/strategy"
	"github.com/ironcore-dev/cloud-hypervisor-provider/internal/vmm"
	"github.com/ironcore-dev/ironcore-image/oci/remote"
	ocistore "github.com/ironcore-dev/ironcore-image/oci/store"
	"github.com/ironcore-dev/ironcore/broker/common"
	commongrpc "github.com/ironcore-dev/ironcore/broker/common/grpc"
	iri "github.com/ironcore-dev/ironcore/iri/apis/machine/v1alpha1"
	"github.com/ironcore-dev/provider-utils/eventutils/event"
	"github.com/ironcore-dev/provider-utils/eventutils/recorder"
	hostutils "github.com/ironcore-dev/provider-utils/storeutils/host"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	homeDir string
)

func init() {
	homeDir, _ = os.UserHomeDir()
}

type Options struct {
	Address string

	RootDir   string
	DetachVms bool

	CloudHypervisorBinPath      string
	CloudHypervisorFirmwarePath string

	NicPlugin *options.Options
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.Address, "address", "/var/run/iri-machinebroker.sock", "Address to listen on.")

	fs.StringVar(
		&o.RootDir,
		"provider-root-dir",
		filepath.Join(homeDir, ".cloud-hypervisor-provider"),
		"Path to the directory where the provider manages its content at.",
	)

	fs.StringVar(
		&o.CloudHypervisorBinPath,
		"cloud-hypervisor-bin-path",
		"",
		"Path to the cloud-hypervisor binary.",
	)

	fs.StringVar(
		&o.CloudHypervisorFirmwarePath,
		"cloud-hypervisor-firmware-path",
		"",
		"Path to the cloud-hypervisor firmware.",
	)

	fs.BoolVar(
		&o.DetachVms,
		"detach-vms",
		true,
		"Detach VMs processes from manager process.",
	)

	o.NicPlugin = options.NewDefaultOptions()
	o.NicPlugin.AddFlags(fs)
}

func Command() *cobra.Command {
	var (
		zapOpts = zap.Options{Development: true}
		opts    Options
	)

	cmd := &cobra.Command{
		Use: "cloud-hypervisor-provider",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			logger := zap.New(zap.UseFlagOptions(&zapOpts))
			ctrl.SetLogger(logger)
			cmd.SetContext(ctrl.LoggerInto(cmd.Context(), ctrl.Log))
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return Run(cmd.Context(), opts)
		},
	}

	goFlags := goflag.NewFlagSet("", 0)
	zapOpts.BindFlags(goFlags)
	cmd.PersistentFlags().AddGoFlagSet(goFlags)

	opts.AddFlags(cmd.Flags())

	return cmd
}

func Run(ctx context.Context, opts Options) error {
	log := ctrl.LoggerFrom(ctx)
	setupLog := log.WithName("setup")

	hostPaths, err := host.PathsAt(opts.RootDir)
	if err != nil {
		setupLog.Error(err, "failed to initialize provider host")
		return err
	}

	reg, err := remote.DockerRegistry(nil)
	if err != nil {
		setupLog.Error(err, "failed to initialize registry")
		return err
	}

	ociStore, err := ocistore.New(hostPaths.ImagesDir())
	if err != nil {
		setupLog.Error(err, "error creating oci store")
		return err
	}

	imgCache, err := oci.NewLocalCache(log, reg, ociStore)
	if err != nil {
		setupLog.Error(err, "failed to initialize oci manager")
		return err
	}

	rawInst, err := raw.Instance(raw.Default())
	if err != nil {
		setupLog.Error(err, "failed to initialize raw instance")
		return err
	}

	pluginManager := volume.NewPluginManager()
	if err := pluginManager.InitPlugins(hostPaths, []volume.Plugin{
		ceph.NewPlugin(ceph.DefaultProvider(
			log.WithName("ceph-volume-plugin"),
			hostPaths,
			//TODO flag
			"/usr/bin/qemu-storage-daemon",
			false,
		)),
	}); err != nil {
		setupLog.Error(err, "failed to initialize plugins")
		return err
	}

	nicPlugin, nicPluginCleanup, err := opts.NicPlugin.NetworkInterfacePlugin()
	if err != nil {
		setupLog.Error(err, "failed to initialize network plugin")
		return err
	}
	if nicPluginCleanup != nil {
		defer nicPluginCleanup()
	}

	if err := nicPlugin.Init(hostPaths); err != nil {
		setupLog.Error(err, "failed to initialize network plugin")
		return err
	}

	machineStore, err := hostutils.NewStore[*api.Machine](hostutils.Options[*api.Machine]{
		Dir:            hostPaths.MachineStoreDir(),
		NewFunc:        func() *api.Machine { return &api.Machine{} },
		CreateStrategy: strategy.MachineStrategy,
	})
	if err != nil {
		setupLog.Error(err, "failed to initialize machine store")
		return err
	}

	machineEvents, err := event.NewListWatchSource[*api.Machine](
		machineStore.List,
		machineStore.Watch,
		event.ListWatchSourceOptions{},
	)
	if err != nil {
		setupLog.Error(err, "failed to initialize machine events")
		return err
	}

	nicStore, err := hostutils.NewStore[*api.NetworkInterface](hostutils.Options[*api.NetworkInterface]{
		Dir:            hostPaths.NICStoreDir(),
		NewFunc:        func() *api.NetworkInterface { return &api.NetworkInterface{} },
		CreateStrategy: strategy.NetworkInterfaceStrategy,
	})
	if err != nil {
		setupLog.Error(err, "failed to initialize nic store")
		return err
	}

	nicEvents, err := event.NewListWatchSource[*api.NetworkInterface](
		nicStore.List,
		nicStore.Watch,
		event.ListWatchSourceOptions{},
	)
	if err != nil {
		setupLog.Error(err, "failed to initialize nic events")
		return err
	}

	eventRecorder := recorder.NewEventStore(log, recorder.EventStoreOptions{})

	virtualMachineManager := vmm.NewManager(hostPaths, vmm.ManagerOptions{
		CloudHypervisorBin: opts.CloudHypervisorBinPath,
		Logger:             log.WithName("virtual-machine-manager"),
		DetachVms:          opts.DetachVms,
		FirmwarePath:       opts.CloudHypervisorFirmwarePath,
	})

	machineReconciler, err := controllers.NewMachineReconciler(
		log.WithName("machine-reconciler"),
		machineStore,
		machineEvents,
		eventRecorder,
		virtualMachineManager,
		pluginManager,
		nicStore,
		nicEvents,
		nicPlugin,
		controllers.MachineReconcilerOptions{
			ImageCache: imgCache,
			Raw:        rawInst,
			Paths:      hostPaths,
		},
	)
	if err != nil {
		setupLog.Error(err, "failed to initialize machine controller")
		return err
	}

	nicReconciler, err := controllers.NewNetworkInterfaceReconciler(
		log.WithName("nic-reconciler"),
		eventRecorder,
		nicStore,
		nicEvents,
		nicPlugin,
		controllers.NetworkInterfaceReconcilerOptions{
			Paths: hostPaths,
		},
	)
	if err != nil {
		setupLog.Error(err, "failed to initialize nic controller")
		return err
	}

	srv, err := server.New(machineStore, server.Options{
		EventStore: eventRecorder,
	})
	if err != nil {
		return fmt.Errorf("error creating server: %w", err)
	}

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		setupLog.Info("Starting oci cache")
		if err := imgCache.Start(ctx); err != nil {
			setupLog.Error(err, "failed to start oci cache")
			return err
		}
		return nil
	})

	g.Go(func() error {
		setupLog.Info("Starting machine reconciler")
		if err := machineReconciler.Start(ctx); err != nil {
			setupLog.Error(err, "failed to start machine reconciler")
			return err
		}
		return nil
	})

	g.Go(func() error {
		setupLog.Info("Starting machine events")
		if err := machineEvents.Start(ctx); err != nil {
			setupLog.Error(err, "failed to start machine events")
			return err
		}
		return nil
	})

	g.Go(func() error {
		setupLog.Info("Starting nic reconciler")
		if err := nicReconciler.Start(ctx); err != nil {
			setupLog.Error(err, "failed to start nic reconciler")
			return err
		}
		return nil
	})

	g.Go(func() error {
		setupLog.Info("Starting nic events")
		if err := nicEvents.Start(ctx); err != nil {
			setupLog.Error(err, "failed to start nic events")
			return err
		}
		return nil
	})

	g.Go(func() error {
		setupLog.Info("Starting machine events garbage collector")
		eventRecorder.Start(ctx)
		return nil
	})

	g.Go(func() error {
		setupLog.Info("Starting grpc server")
		if err := RunGRPCServer(ctx, setupLog, log, srv, opts.Address); err != nil {
			setupLog.Error(err, "failed to start grpc server")
			return err
		}
		return nil
	})
	return g.Wait()
}

func RunGRPCServer(ctx context.Context, setupLog, log logr.Logger, srv *server.Server, address string) error {
	log.V(1).Info("Cleaning up any previous socket")
	if err := common.CleanupSocketIfExists(address); err != nil {
		return fmt.Errorf("error cleaning up socket: %w", err)
	}

	grpcSrv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			commongrpc.InjectLogger(log),
			commongrpc.LogRequest,
		),
	)
	iri.RegisterMachineRuntimeServer(grpcSrv, srv)

	log.V(1).Info("Start listening on unix socket", "Address", address)
	l, err := net.Listen("unix", address)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	setupLog.Info("Starting grpc server", "Address", l.Addr().String())
	go func() {
		<-ctx.Done()
		setupLog.Info("Shutting down grpc server")
		grpcSrv.GracefulStop()
		setupLog.Info("Shut down grpc server")
	}()
	if err := grpcSrv.Serve(l); err != nil {
		return fmt.Errorf("error serving grpc: %w", err)
	}
	return nil
}

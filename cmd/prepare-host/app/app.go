// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	goflag "flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const (
	ChName       = "cloud-hypervisor"
	FirmwareName = "firmware"
	Uid          = 65532 // Desired user ID
	Gid          = 65532 // Desired group ID
)

type Options struct {
	Download bool

	ProviderBasePath string

	CloudHypervisorBinPath   string
	CloudHypervisorBinSubDir string
	CloudHypervisorBinUrl    string

	CloudHypervisorFirmwarePath   string
	CloudHypervisorFirmwareSubDir string
	CloudHypervisorFirmwareUrl    string
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(
		&o.Download,
		"download",
		false,
		"Download binaries otherwise it will error if files are not present.",
	)

	fs.StringVar(
		&o.ProviderBasePath,
		"provider-base-path",
		"/var/lib/cloud-hypervisor-provider",
		"Path to the provider base directory.",
	)

	fs.StringVar(
		&o.CloudHypervisorBinPath,
		"cloud-hypervisor-bin-path",
		"/usr/local/bin/cloud-hypervisor",
		"Path to the cloud-hypervisor binary.",
	)
	fs.StringVar(
		&o.CloudHypervisorBinSubDir,
		"cloud-hypervisor-bin-sub-dir",
		"version",
		"Sub-directory of the cloud-hypervisor binary.",
	)
	fs.StringVar(
		&o.CloudHypervisorBinUrl,
		"cloud-hypervisor-bin-url",
		"",
		"Cloud-hypervisor binary url.",
	)

	fs.StringVar(
		&o.CloudHypervisorFirmwarePath,
		"cloud-hypervisor-firmware-path",
		"/usr/local/bin/cloud-hypervisor-firmware",
		"Path to the cloud-hypervisor firmware.",
	)
	fs.StringVar(
		&o.CloudHypervisorFirmwareSubDir,
		"cloud-hypervisor-firmware-sub-dir",
		"version",
		"Sub-directory of the cloud-hypervisor firmware.",
	)
	fs.StringVar(
		&o.CloudHypervisorFirmwareUrl,
		"cloud-hypervisor-firmware-url",
		"",
		"Cloud-hypervisor firmware url.",
	)
}

func Command() *cobra.Command {
	var (
		zapOpts = zap.Options{Development: true}
		opts    Options
	)

	cmd := &cobra.Command{
		Use: "prepare-host",
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
	log := ctrl.LoggerFrom(ctx).WithName("prepare-host")

	log.Info("starting host preparation")

	log.V(1).Info("checking if base path exists", "path", opts.ProviderBasePath)
	info, err := os.Stat(opts.ProviderBasePath)
	if os.IsNotExist(err) {
		log.V(1).Info("base path does not exist", "path", opts.ProviderBasePath)
		if err := os.MkdirAll(opts.ProviderBasePath, 0755); err != nil {
			return fmt.Errorf("failed to create provider base directory: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to stat %s: %w", opts.ProviderBasePath, err)
	} else if !info.IsDir() {
		return fmt.Errorf("path exists but is not a directory: %s", opts.ProviderBasePath)
	}

	log.V(1).Info("setting owner", "path", opts.ProviderBasePath, "uid", Uid, "gid", Gid)
	if err := os.Chown(opts.ProviderBasePath, Uid, Gid); err != nil {
		return fmt.Errorf("failed to set owner: %w", err)
	}

	chPresent := isFilePresent(log, path.Join(opts.CloudHypervisorBinPath, opts.CloudHypervisorBinSubDir, ChName))
	if !opts.Download && !chPresent {
		log.V(1).Info(
			"cloud-hypervisor binary not present",
			"shouldDownload",
			opts.Download,
			"path",
			path.Join(opts.CloudHypervisorBinPath, opts.CloudHypervisorBinSubDir, ChName),
		)
		return fmt.Errorf("no file present")
	}

	if !chPresent {
		log.Info("downloading cloud-hypervisor binary")
		if err := fetch(
			log,
			opts.CloudHypervisorBinUrl,
			path.Join(opts.CloudHypervisorBinPath, opts.CloudHypervisorBinSubDir),
			ChName,
			true,
		); err != nil {
			return err
		}
	}

	firmwarePresent := isFilePresent(log, path.Join(opts.CloudHypervisorFirmwarePath,
		opts.CloudHypervisorFirmwareSubDir,
		FirmwareName))
	if !opts.Download && !firmwarePresent {
		log.V(1).Info(
			"cloud-hypervisor firmware not present",
			"shouldDownload",
			opts.Download,
			"path",
			path.Join(opts.CloudHypervisorFirmwarePath, opts.CloudHypervisorFirmwareSubDir, FirmwareName),
		)
		return fmt.Errorf("no file present")
	}

	if !firmwarePresent {
		log.Info("downloading cloud-hypervisor firmware")
		if err := fetch(
			log,
			opts.CloudHypervisorFirmwareUrl,
			path.Join(opts.CloudHypervisorFirmwarePath, opts.CloudHypervisorFirmwareSubDir),
			FirmwareName,
			false,
		); err != nil {
			return err
		}
	}

	return nil
}

func fetch(log logr.Logger, fileURL, saveDir, fileName string, isExe bool) error {
	log.V(1).Info("ensure directory exists", "dir", saveDir)
	err := os.MkdirAll(saveDir, os.ModePerm)
	if err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	resp, err := http.Get(fileURL)
	if err != nil {
		return fmt.Errorf("failed to download the file: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	outPath := path.Join(saveDir, fileName)
	outFile, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer func() {
		_ = outFile.Close()
	}()

	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to save the file: %w", err)
	}

	if isExe {
		if err := os.Chmod(outPath, 0755); err != nil {
			return fmt.Errorf("failed to chmod the file: %w", err)
		}
	}
	log.V(1).Info("successfully downloaded", "url", fileURL, "path", outPath)

	return nil
}

func isFilePresent(log logr.Logger, filePath string) bool {
	log.V(1).Info("checking if file exists", "path", filePath)

	_, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.V(1).Info("file does not exist", "path", filePath)
			return false
		}
	}

	log.V(1).Info("file exists", "path", filePath)

	return true
}

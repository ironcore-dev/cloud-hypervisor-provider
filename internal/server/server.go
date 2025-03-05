// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	iri "github.com/ironcore-dev/ironcore/iri/apis/machine/v1alpha1"
)

var _ iri.MachineRuntimeServer = (*Server)(nil)

// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.ironcore.dev,resources=machines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=compute.ironcore.dev,resources=machines/exec,verbs=get;create
// +kubebuilder:rbac:groups=storage.ironcore.dev,resources=volumes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.ironcore.dev,resources=volumes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=networking.ironcore.dev,resources=networkinterfaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.ironcore.dev,resources=networks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.ironcore.dev,resources=networks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=networking.ironcore.dev,resources=virtualips,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.ironcore.dev,resources=virtualips/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=networking.ironcore.dev,resources=loadbalancers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.ironcore.dev,resources=loadbalancers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=networking.ironcore.dev,resources=loadbalancerroutings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.ironcore.dev,resources=natgateways,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.ironcore.dev,resources=natgateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute.ironcore.dev,resources=reservations,verbs=get;list;watch;create;update;patch;delete

type Server struct {
}

type Options struct {
}

func New(opts Options) (*Server, error) {
	_ = opts
	return &Server{}, nil
}

// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package mcr

import (
	"fmt"
)

type MachineClassRegistry interface {
	Get(volumeClassName string) (MachineClass, bool)
	List() []MachineClass
}

type MachineClass struct {
	Name        string
	CpuMillis   int64
	MemoryBytes int64
}

func NewMachineClassRegistry(classes []MachineClass) (*Mcr, error) {
	registry := Mcr{
		classes: map[string]MachineClass{},
	}

	for _, class := range classes {
		if _, ok := registry.classes[class.Name]; ok {
			return nil, fmt.Errorf("multiple classes with same name (%s) found", class.Name)
		}
		registry.classes[class.Name] = class
	}

	return &registry, nil
}

type Mcr struct {
	classes map[string]MachineClass
}

func (m *Mcr) Get(machineClassName string) (MachineClass, bool) {
	class, found := m.classes[machineClassName]
	return class, found
}

func (m *Mcr) List() []MachineClass {
	var classes []MachineClass
	for name := range m.classes {
		class := m.classes[name]
		classes = append(classes, class)
	}
	return classes
}

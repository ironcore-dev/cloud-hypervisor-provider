// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package machineclasses

import (
	"fmt"
)

type Registry interface {
	Get(volumeClassName string) (MachineClass, bool)
	List() []MachineClass
}

type MachineClass struct {
	Name        string
	Cpu         int64
	MemoryBytes int64
	NvidiaGpu   int64
}

func NewRegistry(classes []MachineClass) (*MachineClassRegistry, error) {
	registry := MachineClassRegistry{
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

type MachineClassRegistry struct {
	classes map[string]MachineClass
}

func (m *MachineClassRegistry) Get(machineClassName string) (MachineClass, bool) {
	class, found := m.classes[machineClassName]
	return class, found
}

func (m *MachineClassRegistry) List() []MachineClass {
	var classes []MachineClass
	for name := range m.classes {
		class := m.classes[name]
		classes = append(classes, class)
	}
	return classes
}

// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package machineclasses

import (
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/ironcore-dev/ironcore/api/core/v1alpha1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

type Registry interface {
	Get(machineClassName string) (MachineClass, bool)
	List() []MachineClass
}

type MachineClass struct {
	Name        string
	Cpu         int64
	MemoryBytes int64
	NvidiaGpu   int64
	Resources   v1alpha1.ResourceList
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

func NewRegistryFromFile(file string) (*MachineClassRegistry, error) {
	var machineClasses []MachineClass

	reader, err := os.Open(file)

	if err != nil {
		return nil, fmt.Errorf("failed to open file %s: %w", file, err)
	}

	if err := yaml.NewYAMLOrJSONDecoder(reader, 128).Decode(&machineClasses); err != nil {
		return nil, fmt.Errorf("unable to unmarshal machine classes: %w", err)
	}

	return NewRegistry(machineClasses)
}

type MachineClassRegistry struct {
	classes map[string]MachineClass
}

func (m *MachineClassRegistry) Get(machineClassName string) (MachineClass, bool) {
	class, found := m.classes[machineClassName]
	return class, found
}

func (m *MachineClassRegistry) List() []MachineClass {
	return slices.Collect(maps.Values(m.classes))
}

// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"
	"strconv"
	"strings"
)

type MachineClass struct {
	Name        string
	CpuMillis   int64
	MemoryBytes int64
}
type MachineClassOptions []MachineClass

func (ml *MachineClassOptions) String() string {
	var parts []string
	for _, m := range *ml {
		parts = append(parts, fmt.Sprintf("%s,%d,%d", m.Name, m.CpuMillis, m.MemoryBytes))
	}
	return strings.Join(parts, "; ")
}

func (ml *MachineClassOptions) Set(value string) error {
	parts := strings.Split(value, ",")
	if len(parts) != 3 {
		return fmt.Errorf("invalid machine format: expected name,cpu,memory")
	}

	cpuMillis, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid CPU value: %s", parts[1])
	}

	memoryBytes, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid Memory value: %s", parts[2])
	}

	*ml = append(*ml, MachineClass{
		Name:        parts[0],
		CpuMillis:   cpuMillis,
		MemoryBytes: memoryBytes,
	})

	return nil
}

func (ml *MachineClassOptions) Type() string {
	return "machine-class"
}

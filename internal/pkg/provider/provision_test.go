// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/siderolabs/omni-infra-provider-proxmox/internal/pkg/provider"
)

const talosWorkers = "talos-workers"

func TestPickNode(t *testing.T) {
	const (
		nodeA = "NodeA"
		nodeB = "NodeB"
		nodeC = "NodeC"
		nodeD = "NodeD"
	)

	tests := []struct {
		name     string
		expected string
		input    []provider.NodeStatus
	}{
		{
			name: "Single node should be returned",
			input: []provider.NodeStatus{
				{Name: "node1", MemoryFree: 1, SameMachineRequestSetVMs: 0},
			},
			expected: "node1",
		},
		{
			name: "Primary criteria: Pick node with fewer same-set VMs",
			input: []provider.NodeStatus{
				{Name: nodeA, MemoryFree: 1.0, SameMachineRequestSetVMs: 10},
				// Node B has less memory, but is free (0 VMs) -> Should win
				{Name: nodeB, MemoryFree: 0.5, SameMachineRequestSetVMs: 0},
			},
			expected: nodeB,
		},
		{
			name: "Secondary criteria: If VMs equal, pick node with MOST free memory",
			input: []provider.NodeStatus{
				{Name: nodeA, MemoryFree: 0.5, SameMachineRequestSetVMs: 5},
				{Name: nodeB, MemoryFree: 1.0, SameMachineRequestSetVMs: 5}, // More memory
				{Name: nodeC, MemoryFree: 0.1, SameMachineRequestSetVMs: 5},
			},
			expected: nodeB,
		},
		{
			name: "Complex scenario",
			input: []provider.NodeStatus{
				{Name: nodeA, MemoryFree: 0.1, SameMachineRequestSetVMs: 2},
				{Name: nodeB, MemoryFree: 0.05, SameMachineRequestSetVMs: 1}, // Best VM count
				{Name: nodeC, MemoryFree: 0.04, SameMachineRequestSetVMs: 1}, // Same VM count, less memory than B
				{Name: nodeD, MemoryFree: 1, SameMachineRequestSetVMs: 5},
			},
			expected: nodeB,
		},
		{
			name: "No free memory",
			input: []provider.NodeStatus{
				{Name: nodeA, MemoryFree: 0, SameMachineRequestSetVMs: 0},
				{Name: nodeB, MemoryFree: 1, SameMachineRequestSetVMs: 0},
			},
			expected: nodeB,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			result := provider.PickNode(tt.input)

			// Assert
			require.Equal(t, tt.expected, result.Name)
		})
	}
}

func TestPoolCreateDecision(t *testing.T) {
	tests := []struct {
		name              string
		poolID            string
		machineRequestSet string
		exists            bool
		expectedCreate    bool
		expectedErr       bool
	}{
		{
			name:              "Pool exists: no-op",
			poolID:            "my-pool",
			machineRequestSet: talosWorkers,
			exists:            true,
			expectedCreate:    false,
		},
		{
			name:              "Pool absent, matches machine request set: create",
			poolID:            talosWorkers,
			machineRequestSet: talosWorkers,
			expectedCreate:    true,
		},
		{
			name:              "Pool absent, user-specified: refuse",
			poolID:            "gpu-pool",
			machineRequestSet: talosWorkers,
			expectedErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			create, err := provider.PoolCreateDecision(tt.exists, tt.poolID, tt.machineRequestSet)

			if tt.expectedErr {
				require.Error(t, err)
				require.False(t, create)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expectedCreate, create)
		})
	}
}

func TestBuildTagsOption(t *testing.T) {
	tests := []struct {
		name              string
		machineRequestSet string
		expectedValue     string
		userTags          []string
		expectedOk        bool
	}{
		{
			name:       "No user tags, no request set",
			expectedOk: false,
		},
		{
			name:              "Request set only",
			machineRequestSet: talosWorkers,
			expectedValue:     "machine-request.talos-workers",
			expectedOk:        true,
		},
		{
			name:          "User tags only",
			userTags:      []string{"talos-node", "prod"},
			expectedValue: "talos-node;prod",
			expectedOk:    true,
		},
		{
			name:              "User tags first, internal tag last",
			userTags:          []string{"talos-node", "prod"},
			machineRequestSet: talosWorkers,
			expectedValue:     "talos-node;prod;machine-request.talos-workers",
			expectedOk:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, ok := provider.BuildTagsOption(tt.userTags, tt.machineRequestSet)

			require.Equal(t, tt.expectedOk, ok)
			require.Equal(t, tt.expectedValue, value)
		})
	}
}

func TestSchedulerSpreadsInFlightPlacements(t *testing.T) {
	s := provider.NewScheduler()

	nodes := func() []provider.NodeStatus {
		return []provider.NodeStatus{
			{Name: "node-a", MemoryFree: 0.9},
			{Name: "node-b", MemoryFree: 0.8},
			{Name: "node-c", MemoryFree: 0.7},
		}
	}

	var picked []string

	for _, requestID := range []string{"worker-1", "worker-2", "worker-3"} {
		picked = append(picked, s.Pick(nodes(), talosWorkers, requestID, nil).Name)
	}

	require.ElementsMatch(t, []string{"node-a", "node-b", "node-c"}, picked)
}

func TestSchedulerReleasesMaterializedReservations(t *testing.T) {
	s := provider.NewScheduler()

	twoNodes := func(proxmoxOnA int) []provider.NodeStatus {
		return []provider.NodeStatus{
			{Name: "node-a", MemoryFree: 1.0, SameMachineRequestSetVMs: proxmoxOnA},
			{Name: "node-b", MemoryFree: 0.9},
		}
	}

	require.Equal(t, "node-a", s.Pick(twoNodes(0), talosWorkers, "worker-1", nil).Name)
	require.Equal(t, "node-b", s.Pick(twoNodes(0), talosWorkers, "worker-2", nil).Name)

	picked := s.Pick(twoNodes(1), talosWorkers, "worker-3", map[string]struct{}{"worker-1": {}})

	require.Equal(t, "node-a", picked.Name)
}

func TestSchedulerExpiresStaleReservations(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	s := provider.NewSchedulerWithClock(func() time.Time { return now }, time.Minute)

	nodes := func() []provider.NodeStatus {
		return []provider.NodeStatus{
			{Name: "node-a", MemoryFree: 1.0},
			{Name: "node-b", MemoryFree: 0.9},
		}
	}

	require.Equal(t, "node-a", s.Pick(nodes(), talosWorkers, "worker-1", nil).Name)

	now = now.Add(2 * time.Minute)

	require.Equal(t, "node-a", s.Pick(nodes(), talosWorkers, "worker-2", nil).Name)
}

// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"cmp"
	"fmt"
	"slices"
	"sync"
	"time"
)

const reservationTTL = time.Hour

type placementStrategy string

const (
	strategySpread     placementStrategy = "spread"
	strategyFewerVMs   placementStrategy = "fewer-vms"
	strategyRoundRobin placementStrategy = "round-robin"
	strategyBinpack    placementStrategy = "binpack"
)

func parseStrategy(s string) (placementStrategy, error) {
	switch placementStrategy(s) {
	case "", strategySpread:
		return strategySpread, nil
	case strategyFewerVMs, strategyRoundRobin, strategyBinpack:
		return placementStrategy(s), nil
	default:
		return "", fmt.Errorf("unknown placement strategy %q", s)
	}
}

type reservation struct {
	at     time.Time
	node   string
	set    string
	memory uint64
}

// scheduler accounts for in-flight placements so concurrent VMs in a machine
// request set are distributed by the chosen strategy instead of clumping.
type scheduler struct {
	now          func() time.Time
	reservations map[string]reservation
	ttl          time.Duration
	mu           sync.Mutex
}

func newScheduler() *scheduler {
	return newSchedulerWithClock(time.Now, reservationTTL)
}

func newSchedulerWithClock(now func() time.Time, ttl time.Duration) *scheduler {
	return &scheduler{
		now:          now,
		reservations: map[string]reservation{},
		ttl:          ttl,
	}
}

func (s *scheduler) pick(nodes []nodeStatus, set, requestID string, memory uint64, strat placementStrategy, materialized map[string]struct{}) nodeStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()

	for id, r := range s.reservations {
		if _, done := materialized[id]; done || now.Sub(r.at) > s.ttl {
			delete(s.reservations, id)
		}
	}

	byName := make(map[string]*nodeStatus, len(nodes))
	for i := range nodes {
		byName[nodes[i].Name] = &nodes[i]
	}

	for id, r := range s.reservations {
		if id == requestID || r.set != set {
			continue
		}

		if ns, ok := byName[r.node]; ok {
			applyReservation(strat, ns, r)
		}
	}

	picked := selectNode(strat, nodes, memory)

	s.reservations[requestID] = reservation{node: picked.Name, set: set, memory: memory, at: now}

	return picked
}

// release drops a request's reservation when its VM is deprovisioned, so a node
// removed before the VM materializes doesn't strand ledger data until the TTL.
func (s *scheduler) release(requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.reservations, requestID)
}

func applyReservation(strat placementStrategy, ns *nodeStatus, r reservation) {
	switch strat {
	case strategyFewerVMs:
		ns.TotalVMs++
	case strategyBinpack:
		// FreeMem is unsigned; an over-reserved node ranks as full, not wrapped.
		if ns.FreeMem >= r.memory {
			ns.FreeMem -= r.memory
		} else {
			ns.FreeMem = 0
		}
	case strategySpread, strategyRoundRobin:
		ns.SameMachineRequestSetVMs++
	}
}

func selectNode(strat placementStrategy, nodes []nodeStatus, memory uint64) nodeStatus {
	switch strat {
	case strategyFewerVMs:
		slices.SortFunc(nodes, func(a, b nodeStatus) int {
			if c := cmp.Compare(a.TotalVMs, b.TotalVMs); c != 0 {
				return c
			}

			return -cmp.Compare(a.MemoryFree, b.MemoryFree)
		})

		return nodes[0]
	case strategyRoundRobin:
		slices.SortFunc(nodes, func(a, b nodeStatus) int {
			if c := cmp.Compare(a.SameMachineRequestSetVMs, b.SameMachineRequestSetVMs); c != 0 {
				return c
			}

			return cmp.Compare(a.Name, b.Name)
		})

		return nodes[0]
	case strategyBinpack:
		return pickBinpack(nodes, memory)
	case strategySpread:
		return pickNode(nodes)
	default:
		return pickNode(nodes)
	}
}

// Prefer the most-loaded node that still fits the requested memory, falling back
// to the node with the most free memory when none fit.
func pickBinpack(nodes []nodeStatus, memory uint64) nodeStatus {
	slices.SortFunc(nodes, func(a, b nodeStatus) int {
		aFits, bFits := a.FreeMem >= memory, b.FreeMem >= memory

		if aFits != bFits {
			if aFits {
				return -1
			}

			return 1
		}

		if aFits {
			return cmp.Compare(a.FreeMem, b.FreeMem)
		}

		return -cmp.Compare(a.FreeMem, b.FreeMem)
	})

	return nodes[0]
}

// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"sync"
	"time"
)

// reservationTTL backstops placements whose VM never becomes visible in Proxmox.
const reservationTTL = time.Hour

type reservation struct {
	at   time.Time
	node string
	set  string
}

// scheduler accounts for in-flight placements so concurrent VMs in a machine
// request set spread across nodes instead of clumping onto one.
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

func (s *scheduler) pick(nodes []nodeStatus, set, requestID string, materialized map[string]struct{}) nodeStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()

	for id, r := range s.reservations {
		if _, done := materialized[id]; done || now.Sub(r.at) > s.ttl {
			delete(s.reservations, id)
		}
	}

	inFlight := map[string]int{}

	for id, r := range s.reservations {
		if id == requestID || r.set != set {
			continue
		}

		inFlight[r.node]++
	}

	for i := range nodes {
		nodes[i].SameMachineRequestSetVMs += inFlight[nodes[i].Name]
	}

	picked := pickNode(nodes)

	s.reservations[requestID] = reservation{node: picked.Name, set: set, at: now}

	return picked
}

// release drops a request's reservation when its VM is deprovisioned, so a node
// removed before the VM materializes doesn't strand ledger data until the TTL.
func (s *scheduler) release(requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.reservations, requestID)
}

// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package ha_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/luthermonson/go-proxmox"
	"github.com/siderolabs/omni/client/pkg/infra/provision"
	"github.com/siderolabs/omni/client/pkg/omni/resources/infra"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/siderolabs/omni-infra-provider-proxmox/internal/pkg/provider/ha"
	"github.com/siderolabs/omni-infra-provider-proxmox/internal/pkg/provider/resources"
)

const (
	pve1        = "pve1"
	vm100       = "vm:100"
	vm101       = "vm:101"
	affNegative = "negative"
	raffRule    = "/cluster/ha/rules/omni-set1-raff"
	naffRule    = "omni-set1-naff"
	nodeOnline  = "online"

	fSID       = "sid"
	fRule      = "rule"
	fType      = "type"
	fResources = "resources"
	fDigest    = "digest"
	fNode      = "node"
	fVMID      = "vmid"
	fStatus    = "status"

	typeResourceAffinity = "resource-affinity"
	digestStub           = "abc"

	ruleCustom = "custom-rule"
)

func newManager(t *testing.T, mux *http.ServeMux) *ha.Manager {
	t.Helper()

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return ha.NewManager(proxmox.NewClient(srv.URL))
}

func writeData(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
}

func fail500(t *testing.T, w http.ResponseWriter, reason string) {
	t.Helper()

	hj, ok := w.(http.Hijacker)
	require.True(t, ok)

	conn, bufrw, err := hj.Hijack()
	require.NoError(t, err)

	defer conn.Close() //nolint:errcheck

	_, err = bufrw.WriteString("HTTP/1.1 500 " + reason + "\r\nContent-Length: 0\r\n\r\n")
	require.NoError(t, err)
	require.NoError(t, bufrw.Flush())
}

func TestConfigValidate(t *testing.T) {
	require.NoError(t, (&ha.Config{
		State: "started", ResourceAffinity: affNegative,
		Rules: []string{"valid-rule_1"}, NodeAffinityNodes: []string{pve1},
	}).Validate())

	for name, cfg := range map[string]*ha.Config{
		"bad state":            {State: "bogus"},
		"bad affinity":         {ResourceAffinity: "sideways"},
		"rule path traversal":  {Rules: []string{"../escape"}},
		"rule comma injection": {Rules: []string{"a,b"}},
		"rule leading symbol":  {Rules: []string{"_rule"}},
		"node comma injection": {NodeAffinityNodes: []string{"pve1,pve2"}},
		"node underscore":      {NodeAffinityNodes: []string{"node_1"}},
		"node whitespace":      {NodeAffinityNodes: []string{"pve 1"}},
	} {
		t.Run(name, func(t *testing.T) { require.Error(t, cfg.Validate()) })
	}
}

func TestResourceAffinityDeferredUntilTwoMembers(t *testing.T) {
	var created atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc(raffRule, func(w http.ResponseWriter, _ *http.Request) {
		fail500(t, w, "no such ha rule 'omni-set1-raff'")
	})
	mux.HandleFunc("/cluster/ha/rules", func(w http.ResponseWriter, _ *http.Request) {
		created.Add(1)
		writeData(t, w, nil)
	})
	mux.HandleFunc("/cluster/ha/resources", func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, []map[string]any{{fSID: vm100}})
	})
	mux.HandleFunc("/nodes", func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, []map[string]any{})
	})

	require.NoError(t, newManager(t, mux).SyncRules(context.Background(), zap.NewNop(), "set1", vm100, ha.Config{ResourceAffinity: affNegative}))
	require.Zero(t, created.Load(), "rule must not be created with a single member")
}

func TestResourceAffinityCreatedAtTwoMembers(t *testing.T) {
	var got map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc(raffRule, func(w http.ResponseWriter, _ *http.Request) {
		fail500(t, w, "no such ha rule 'omni-set1-raff'")
	})
	mux.HandleFunc("/cluster/ha/rules", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		writeData(t, w, nil)
	})
	mux.HandleFunc("/cluster/ha/resources", func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, []map[string]any{{fSID: vm100}, {fSID: vm101}})
	})
	mux.HandleFunc("/nodes", func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, []map[string]any{{fNode: pve1, fStatus: nodeOnline}})
	})
	mux.HandleFunc("/nodes/pve1/status", func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, map[string]any{})
	})
	mux.HandleFunc("/nodes/pve1/qemu", func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, []map[string]any{
			{fVMID: 100, "tags": "machine-request.set1"},
			{fVMID: 101, "tags": "machine-request.set1"},
		})
	})

	require.NoError(t, newManager(t, mux).SyncRules(context.Background(), zap.NewNop(), "set1", vm100, ha.Config{ResourceAffinity: affNegative}))
	require.Equal(t, typeResourceAffinity, got[fType])
	require.Equal(t, affNegative, got["affinity"])
	require.Equal(t, "vm:100,vm:101", got[fResources])
}

func TestRuleEditRetriesOnDigestConflict(t *testing.T) {
	var (
		gets atomic.Int32
		puts atomic.Int32
	)

	retriedDigest := make(chan string, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/cluster/ha/rules/omni-set1-naff", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

			require.Equal(t, "node-affinity", body[fType])
			require.Equal(t, pve1, body["nodes"])
			require.EqualValues(t, 1, body["strict"])

			if puts.Add(1) == 1 {
				fail500(t, w, "detected modified configuration - try again")

				return
			}

			retriedDigest <- fmt.Sprint(body[fDigest])

			writeData(t, w, nil)

			return
		}

		writeData(t, w, map[string]any{
			fType: "node-affinity", fRule: naffRule, fResources: vm100,
			"nodes": pve1, fDigest: fmt.Sprintf("digest-%d", gets.Add(1)),
			"strict": 1,
		})
	})

	require.NoError(t, newManager(t, mux).SyncRules(context.Background(), zap.NewNop(), "set1", vm101, ha.Config{
		Rules: []string{naffRule},
	}))
	require.Equal(t, int32(2), puts.Load(), "PUT must be retried once after the digest conflict")
	require.Equal(t, int32(2), gets.Load(), "the retry must re-read the rule for a fresh digest")
	require.Equal(t, "digest-2", <-retriedDigest, "the retried PUT must carry the re-read digest")
}

func TestNodeAffinityCreatedWhenNodesSet(t *testing.T) {
	var got map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("/cluster/ha/rules", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		writeData(t, w, nil)
	})

	strict := true
	require.NoError(t, newManager(t, mux).SyncRules(context.Background(), zap.NewNop(), "set1", vm100, ha.Config{
		NodeAffinityNodes: []string{pve1}, NodeAffinityStrict: &strict,
	}))
	require.Equal(t, "node-affinity", got[fType])
	require.Equal(t, vm100, got[fResources])
	require.Equal(t, pve1, got["nodes"])
	require.EqualValues(t, 1, got["strict"])
}

func TestNodeAffinityAdoptsExistingRule(t *testing.T) {
	var put map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("/cluster/ha/rules", func(w http.ResponseWriter, _ *http.Request) {
		fail500(t, w, "ha rule 'omni-set1-naff' already defined")
	})
	mux.HandleFunc("/cluster/ha/rules/omni-set1-naff", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&put))
			writeData(t, w, nil)

			return
		}

		writeData(t, w, map[string]any{
			fType: "node-affinity", fRule: naffRule, fResources: vm100,
			"nodes": "pve2", fDigest: digestStub,
		})
	})

	require.NoError(t, newManager(t, mux).SyncRules(context.Background(), zap.NewNop(), "set1", vm101, ha.Config{
		NodeAffinityNodes: []string{pve1},
	}))
	require.Equal(t, vm100+","+vm101, put[fResources])
	require.Equal(t, "pve2", put["nodes"])
}

func TestRegisterReconcilesExistingResource(t *testing.T) {
	var updated atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/cluster/ha/resources", func(w http.ResponseWriter, _ *http.Request) {
		fail500(t, w, "ha resource 'vm:100' already defined")
	})
	mux.HandleFunc("/cluster/ha/resources/vm:100", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPut, r.Method)
		updated.Store(true)
		writeData(t, w, nil)
	})

	require.NoError(t, newManager(t, mux).Register(context.Background(), vm100, ha.Config{State: "started"}))
	require.True(t, updated.Load(), "an existing resource must be reconciled via PUT")
}

func TestRegisterStepRejectsHARemovedFromRegistered(t *testing.T) {
	st := resources.NewMachine("test-ns", "test")
	st.TypedSpec().Value.HaRegistered = true

	pctx := provision.NewContext(
		infra.NewMachineRequest("test-req"),
		infra.NewMachineRequestStatus("test-req"),
		st,
		provision.ConnectionParams{},
		nil,
		nil,
	)

	err := newManager(t, http.NewServeMux()).RegisterStep(context.Background(), zap.NewNop(), pctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ha config removed")
}

func TestRuleEditEchoesResourceAffinityFields(t *testing.T) {
	var got map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("/cluster/ha/rules/custom-rule", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
			writeData(t, w, nil)

			return
		}

		writeData(t, w, map[string]any{
			fType: typeResourceAffinity, fRule: ruleCustom,
			fResources: vm100, "affinity": affNegative, fDigest: digestStub,
		})
	})

	require.NoError(t, newManager(t, mux).SyncRules(context.Background(), zap.NewNop(), "set1", vm101, ha.Config{
		Rules: []string{ruleCustom},
	}))
	require.Equal(t, typeResourceAffinity, got[fType])
	require.Equal(t, affNegative, got["affinity"])
	require.Equal(t, vm100+","+vm101, got[fResources])
	require.Equal(t, digestStub, got[fDigest])
}

func TestSyncRulesFailsWhenRuleAPIAbsent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/cluster/ha/rules", func(w http.ResponseWriter, _ *http.Request) {
		fail500(t, w, "Method 'POST /cluster/ha/rules' not implemented")
	})

	err := newManager(t, mux).SyncRules(context.Background(), zap.NewNop(), "set1", vm100, ha.Config{
		NodeAffinityNodes: []string{pve1},
	})
	require.Error(t, err)
}

func TestFindVMNode(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/nodes", func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, []map[string]any{
			{fNode: pve1, fStatus: nodeOnline},
			{fNode: "pve2", fStatus: nodeOnline},
		})
	})
	mux.HandleFunc("/nodes/pve1/status", func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, map[string]any{})
	})
	mux.HandleFunc("/nodes/pve2/status", func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, map[string]any{})
	})
	mux.HandleFunc("/nodes/pve1/qemu", func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, []map[string]any{{fVMID: 100}})
	})
	mux.HandleFunc("/nodes/pve2/qemu", func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, []map[string]any{{fVMID: 119}})
	})

	m := newManager(t, mux)

	node, found, err := m.FindVMNode(context.Background(), 119)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "pve2", node)

	_, found, err = m.FindVMNode(context.Background(), 42)
	require.NoError(t, err)
	require.False(t, found)
}

func TestTeardownDeregistersWithPurge(t *testing.T) {
	var deregistered atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/cluster/ha/resources/vm:100", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		require.Equal(t, "1", r.URL.Query().Get("purge"))
		deregistered.Store(true)
		writeData(t, w, nil)
	})

	require.NoError(t, newManager(t, mux).Teardown(context.Background(), vm100))
	require.True(t, deregistered.Load())
}

func TestTeardownToleratesAlreadyDeregistered(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/cluster/ha/resources/vm:120", func(w http.ResponseWriter, _ *http.Request) {
		fail500(t, w, "cannot delete service 'vm:120', not HA managed!")
	})

	require.NoError(t, newManager(t, mux).Teardown(context.Background(), "vm:120"))
}

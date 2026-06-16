// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package ha adds Proxmox HA support: it registers VMs as HA resources and
// maintains node-affinity / resource-affinity rules for a machine-request set.
package ha

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/luthermonson/go-proxmox"
	"github.com/siderolabs/omni/client/pkg/infra/provision"
	"go.uber.org/zap"

	"github.com/siderolabs/omni-infra-provider-proxmox/internal/pkg/provider/resources"
)

const (
	nodeAffinitySuffix     = "naff"
	resourceAffinitySuffix = "raff"
	machineRequestTag      = "machine-request."
	nodeOnline             = "online"
)

// ruleRetries bounds the digest optimistic-locking retries when a rule changes
// between our read and write (e.g. an admin or PVE's HA stack editing it).
const ruleRetries = 10

var ruleNameInvalid = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// classify maps Proxmox's 500 reason-phrases onto these sentinels, wrapping
// the original so errors.Is matches both. go-proxmox has no typed errors for
// them; the raw status line is the only contract.
var (
	errNotFound      = errors.New("ha: not found")
	errAlreadyExists = errors.New("ha: already exists")
	errConflict      = errors.New("ha: digest conflict")
)

func classify(err error) error {
	if err == nil {
		return nil
	}

	// Match HA-specific phrasings only: a generic "already exists" could
	// misclassify an unrelated error and e.g. orphan a resource.
	switch s := err.Error(); {
	case strings.Contains(s, "no such resource"),
		strings.Contains(s, "no such ha rule"),
		strings.Contains(s, "not HA managed"):
		return fmt.Errorf("%w: %w", errNotFound, err)
	case strings.Contains(s, "already defined"):
		return fmt.Errorf("%w: %w", errAlreadyExists, err)
	case strings.Contains(s, "detected modified configuration"):
		return fmt.Errorf("%w: %w", errConflict, err)
	}

	return err
}

// Config is the ha: block of a machine class config. A nil *Config means HA
// is not requested for the machine.
type Config struct {
	MaxRestart         *int     `yaml:"max_restart,omitempty"`
	MaxRelocate        *int     `yaml:"max_relocate,omitempty"`
	NodeAffinityStrict *bool    `yaml:"node_affinity_strict,omitempty"`
	Failback           *bool    `yaml:"failback,omitempty"`
	State              string   `yaml:"state,omitempty"`
	Comment            string   `yaml:"comment,omitempty"`
	ResourceAffinity   string   `yaml:"resource_affinity,omitempty"`
	Rules              []string `yaml:"rules,omitempty"`
	NodeAffinityNodes  []string `yaml:"node_affinity_nodes,omitempty"`
}

func createOption(cfg Config, sid string) *proxmox.HAResourceCreateOption {
	opt := &proxmox.HAResourceCreateOption{
		SID:         sid,
		Comment:     cfg.Comment,
		MaxRestart:  cfg.MaxRestart,
		MaxRelocate: cfg.MaxRelocate,
	}

	if cfg.State != "" {
		opt.State = &cfg.State
	}

	if cfg.Failback != nil {
		fb := proxmox.IntOrBool(*cfg.Failback)
		opt.Failback = &fb
	}

	return opt
}

// updateOption maps the ha: block onto the HA resource PUT form, or nil when
// the config sets none of the mutable fields so reconcile can skip the PUT.
func updateOption(cfg Config) *proxmox.HAResourceUpdateOption {
	if cfg.State == "" && cfg.Comment == "" && cfg.MaxRestart == nil &&
		cfg.MaxRelocate == nil && cfg.Failback == nil {
		return nil
	}

	opt := &proxmox.HAResourceUpdateOption{
		Comment:     cfg.Comment,
		MaxRestart:  cfg.MaxRestart,
		MaxRelocate: cfg.MaxRelocate,
	}

	if cfg.State != "" {
		opt.State = &cfg.State
	}

	if cfg.Failback != nil {
		fb := proxmox.IntOrBool(*cfg.Failback)
		opt.Failback = &fb
	}

	return opt
}

// Manager registers VMs as Proxmox HA resources and maintains per-set affinity rules.
type Manager struct {
	px      *proxmox.Client
	cluster *proxmox.Cluster
}

// NewManager builds a Manager driving the /cluster/ha API on px. The cluster
// handle is built without I/O: px.Cluster would issue a /cluster/status round
// trip on every call, which the digest retry loop would multiply.
func NewManager(px *proxmox.Client) *Manager {
	return &Manager{px: px, cluster: new(proxmox.Cluster).New(px)}
}

// Register adds the VM (sid "vm:<id>") as an HA resource, adopting one that
// already exists (a retry, or admin pre-created it).
func (m *Manager) Register(ctx context.Context, sid string, cfg Config) error {
	switch err := classify(m.cluster.NewHAResource(ctx, createOption(cfg, sid))); {
	case err == nil:
		return nil
	case errors.Is(err, errAlreadyExists):
		// Reconcile the mutable fields so an edited ha: block takes effect,
		// matching how the affinity rules reconcile on every sync.
		opt := updateOption(cfg)
		if opt == nil {
			return nil
		}

		if updErr := classify(m.cluster.HAResourceUpdate(ctx, sid, opt)); updErr != nil {
			return fmt.Errorf("failed to update HA resource %s: %w", sid, updErr)
		}

		return nil
	default:
		return fmt.Errorf("failed to register HA resource %s: %w", sid, err)
	}
}

// SyncRules adds sid to the set's node-affinity and resource-affinity rules
// (creating them on demand) and to any user-referenced rules. Requires PVE 9+.
func (m *Manager) SyncRules(ctx context.Context, logger *zap.Logger, set, sid string, cfg Config) error {
	// The per-set affinity rules are named after the set, so they only apply to a
	// machine that belongs to one; the user-listed rules are set-independent.
	if set != "" {
		if len(cfg.NodeAffinityNodes) > 0 {
			if err := m.ensureNodeAffinity(ctx, logger, set, sid, cfg); err != nil {
				return err
			}
		}

		if cfg.ResourceAffinity != "" {
			if err := m.ensureResourceAffinity(ctx, set, sid, cfg); err != nil {
				return err
			}
		}
	}

	for _, name := range cfg.Rules {
		if err := m.addToRule(ctx, name, sid); err != nil {
			return err
		}
	}

	return nil
}

// Teardown deregisters the HA resource with purge, which strips the sid from
// every rule server-side and drops a rule that would become invalid — PVE's
// documented behavior, not reimplemented client-side.
func (m *Manager) Teardown(ctx context.Context, sid string) error {
	if err := classify(m.cluster.HAResourceDelete(ctx, sid, true)); err != nil && !errors.Is(err, errNotFound) {
		return fmt.Errorf("failed to deregister HA resource %s: %w", sid, err)
	}

	return nil
}

// FindVMNode finds the node hosting vmid by scanning the cluster: HA may have
// migrated it off its provisioned node, so the persisted node is not authoritative.
func (m *Manager) FindVMNode(ctx context.Context, vmid int32) (string, bool, error) {
	nodes, err := m.px.Nodes(ctx)
	if err != nil {
		return "", false, fmt.Errorf("failed to list nodes: %w", err)
	}

	for _, ns := range nodes {
		if ns.Status != nodeOnline {
			continue
		}

		node, err := m.px.Node(ctx, ns.Node)
		if err != nil {
			return "", false, fmt.Errorf("failed to get node %q: %w", ns.Node, err)
		}

		vms, err := node.VirtualMachines(ctx)
		if err != nil {
			return "", false, fmt.Errorf("failed to list vms on %q: %w", ns.Node, err)
		}

		for _, vm := range vms {
			if uint64(vm.VMID) == uint64(vmid) {
				return ns.Node, true, nil
			}
		}
	}

	return "", false, nil
}

// ensureNodeAffinity creates the node-affinity rule with sid as a member, or
// adds sid to the existing one. Node-affinity is valid with a single member.
func (m *Manager) ensureNodeAffinity(ctx context.Context, logger *zap.Logger, set, sid string, cfg Config) error {
	name := ruleName(set, nodeAffinitySuffix)
	nodes := strings.Join(cfg.NodeAffinityNodes, ",")

	switch err := classify(m.cluster.NewHARule(ctx, &proxmox.HARuleCreateOption{
		Rule:      name,
		Type:      "node-affinity",
		Resources: sid,
		Nodes:     nodes,
		Strict:    proxmox.IntOrBool(cfg.NodeAffinityStrict != nil && *cfg.NodeAffinityStrict),
	})); {
	case err == nil:
		return nil
	case !errors.Is(err, errAlreadyExists):
		return fmt.Errorf("failed to create HA rule %q: %w", name, err)
	}

	if rule, err := m.getRule(ctx, name); err == nil && rule.Nodes != nodes {
		logger.Warn("node-affinity rule node set differs from machine config; the rule is created once, edit it in Proxmox to change the nodes",
			zap.String("rule", name), zap.String("config_nodes", nodes), zap.String("rule_nodes", rule.Nodes))
	}

	return m.addToRule(ctx, name, sid)
}

// ensureResourceAffinity adds sid to the resource-affinity rule, creating it
// once the set has the two members Proxmox requires (a no-op before that).
func (m *Manager) ensureResourceAffinity(ctx context.Context, set, sid string, cfg Config) error {
	name := ruleName(set, resourceAffinitySuffix)

	switch err := m.addToRule(ctx, name, sid); {
	case err == nil:
		return nil
	case !errors.Is(err, errNotFound):
		return err
	}

	members, err := m.setMembers(ctx, set, sid)
	if err != nil {
		return err
	}

	if len(members) < 2 {
		return nil
	}

	return m.createOrAdopt(ctx, name, sid, func() error {
		return classify(m.cluster.NewHARule(ctx, &proxmox.HARuleCreateOption{
			Rule:      name,
			Type:      "resource-affinity",
			Resources: strings.Join(members, ","),
			Affinity:  cfg.ResourceAffinity,
		}))
	})
}

// createOrAdopt runs create; on success returns nil, on an already-exists error
// adds sid to the rule another set member created first, otherwise wraps.
func (m *Manager) createOrAdopt(ctx context.Context, name, sid string, create func() error) error {
	switch err := create(); {
	case err == nil:
		return nil
	case errors.Is(err, errAlreadyExists):
		return m.addToRule(ctx, name, sid)
	default:
		return fmt.Errorf("failed to create HA rule %q: %w", name, err)
	}
}

// addToRule adds sid to the rule's members with the digest optimistic-locking
// retry Proxmox requires. PVE resets any field omitted from a rule PUT (and
// rejects a resource-affinity PUT that drops affinity), so the rule's managed
// fields are echoed back alongside the new member list.
func (m *Manager) addToRule(ctx context.Context, name, sid string) error {
	for range ruleRetries {
		rule, err := m.getRule(ctx, name)
		if err != nil {
			return err
		}

		members := splitCSV(rule.Resources)
		if slices.Contains(members, sid) {
			return nil
		}

		err = classify(m.cluster.HARuleUpdate(ctx, name, &proxmox.HARuleUpdateOption{
			Type:      rule.Type,
			Digest:    rule.Digest,
			Resources: strings.Join(append(members, sid), ","),
			Affinity:  rule.Affinity,
			Nodes:     rule.Nodes,
			Strict:    rule.Strict,
		}))

		switch {
		case err == nil:
			return nil
		case !errors.Is(err, errConflict):
			return fmt.Errorf("failed to add %s to HA rule %q: %w", sid, name, err)
		}
	}

	return fmt.Errorf("gave up editing HA rule %q after %d retries: %w", name, ruleRetries, errConflict)
}

// getRule reads a rule; an absent rule surfaces as errNotFound.
func (m *Manager) getRule(ctx context.Context, name string) (*proxmox.HARule, error) {
	rule, err := m.cluster.HARule(ctx, name)
	if err != nil {
		return nil, classify(err)
	}

	return rule, nil
}

// setMembers returns the set's HA-registered sids (self included), found via the
// machine-request.<set> tag and intersected with the resource list so no
// unmanaged sid is returned. Tags are read live per node, not from the cached
// /cluster/resources aggregate, so a member created earlier in this reconcile
// wave is still seen (else the two-member resource-affinity rule never forms).
func (m *Manager) setMembers(ctx context.Context, set, self string) ([]string, error) {
	regs, err := m.cluster.HAResources(ctx, "")
	if err = classify(err); err != nil && !errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("failed to list HA resources: %w", err)
	}

	registered := make(map[string]struct{}, len(regs))
	for _, r := range regs {
		if r != nil && r.SID != "" {
			registered[r.SID] = struct{}{}
		}
	}

	nodes, err := m.px.Nodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	want := machineRequestTag + set
	members := map[string]struct{}{}

	// Only seed self once it is itself HA-registered, keeping the invariant that
	// every returned sid is HA-managed (RegisterStep runs before this step).
	if _, ok := registered[self]; ok {
		members[self] = struct{}{}
	}

	for _, ns := range nodes {
		if ns.Status != nodeOnline {
			continue
		}

		node, err := m.px.Node(ctx, ns.Node)
		if err != nil {
			return nil, fmt.Errorf("failed to get node %q: %w", ns.Node, err)
		}

		vms, err := node.VirtualMachines(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list vms on %q: %w", ns.Node, err)
		}

		for _, vm := range vms {
			sid := fmt.Sprintf("vm:%d", vm.VMID)
			if _, ok := registered[sid]; ok && hasTag(vm.Tags, want) {
				members[sid] = struct{}{}
			}
		}
	}

	return slices.Sorted(maps.Keys(members)), nil
}

// hasTag splits the raw tag CSV directly: go-proxmox's VirtualMachine.HasTag
// reports false for VMs from the list endpoint.
func hasTag(tags, tag string) bool {
	return slices.Contains(strings.Split(tags, proxmox.TagSeperator), tag)
}

func ruleName(set, suffix string) string {
	return "omni-" + ruleNameInvalid.ReplaceAllString(set, "-") + "-" + suffix
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}

	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))

	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}

	return out
}

type machineData struct {
	HA *Config `yaml:"ha,omitempty"`
}

// config returns the HA config and sid for the machine, or ok=false when HA is
// not requested. HA steps run after the VM is created, so the vmid is assigned.
func (m *Manager) config(pctx provision.Context[*resources.Machine]) (*Config, string, bool, error) {
	var data machineData

	if err := pctx.UnmarshalProviderData(&data); err != nil {
		return nil, "", false, err
	}

	if data.HA == nil {
		// Dropping ha: from a machine we already registered would orphan the HA
		// resource (we would never deregister it). Fail loudly instead.
		if pctx.State.TypedSpec().Value.HaRegistered {
			return nil, "", false, errors.New("ha config removed from an HA-registered machine; re-add ha: or deprovision it")
		}

		return nil, "", false, nil
	}

	vmid := pctx.State.TypedSpec().Value.Vmid
	if vmid == 0 {
		return nil, "", false, errors.New("vmid not assigned; HA steps must run after the VM is created")
	}

	return data.HA, fmt.Sprintf("vm:%d", vmid), true, nil
}

// RegisterStep registers the VM as an HA resource (a provision.Step).
func (m *Manager) RegisterStep(ctx context.Context, logger *zap.Logger, pctx provision.Context[*resources.Machine]) error {
	cfg, sid, ok, err := m.config(pctx)
	if err != nil || !ok {
		return err
	}

	if err := m.Register(ctx, sid, *cfg); err != nil {
		return err
	}

	logger.Info("registered VM as Proxmox HA resource", zap.String("sid", sid))

	// HaRegistered persists on this step's nil return (COSI commit boundary) so
	// Deprovision knows to deregister even if a later step fails.
	pctx.State.TypedSpec().Value.HaRegistered = true

	return nil
}

// SyncRulesStep maintains the set's affinity rules (a provision.Step).
// Requires PVE 9+; on older clusters the rule API is absent and the step fails.
func (m *Manager) SyncRulesStep(ctx context.Context, logger *zap.Logger, pctx provision.Context[*resources.Machine]) error {
	cfg, sid, ok, err := m.config(pctx)
	if err != nil || !ok {
		return err
	}

	// A machine outside any request set still gets its user-listed rules; only the
	// per-set affinity rules need the set id, so an empty set just skips those.
	set, _ := pctx.GetMachineRequestSetID()

	return m.SyncRules(ctx, logger, set, sid, *cfg)
}

// Deprovision deregisters the HA resource (purging its rule memberships)
// before the VM is deleted. No-op for VMs that were never HA-registered.
func (m *Manager) Deprovision(ctx context.Context, _ *zap.Logger, machine *resources.Machine) error {
	if !machine.TypedSpec().Value.HaRegistered {
		return nil
	}

	return m.Teardown(ctx, fmt.Sprintf("vm:%d", machine.TypedSpec().Value.Vmid))
}

// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

type NodeStatus = nodeStatus

func PickNode(nodes []NodeStatus) NodeStatus {
	return pickNode(nodes)
}

func BuildTagsOption(userTags []string, machineRequestSet string) (string, bool) {
	return buildTagsOption(userTags, machineRequestSet)
}

func PoolCreateDecision(exists bool, poolID, machineRequestSet string) (bool, error) {
	return poolCreateDecision(exists, poolID, machineRequestSet)
}

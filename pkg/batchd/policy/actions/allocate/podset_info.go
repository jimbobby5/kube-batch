/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package allocate

import (
	"k8s.io/api/core/v1"

	"github.com/golang/glog"
	"github.com/kubernetes-incubator/kube-arbitrator/pkg/batchd/cache"
	"github.com/kubernetes-incubator/kube-arbitrator/pkg/batchd/policy/util"
)

type podSetInfo struct {
	podSet *cache.JobInfo

	dominantResource v1.ResourceName // The dominant resource name of PodSet
	allocated        *cache.Resource // Allocated resource of PodSet
	share            float64         // The DRF share of PodSet
	total            *cache.Resource // The total resource of cluster, used to update DRF share

	unacceptedAllocated    *cache.Resource
	unacceptedAssignedPods []*cache.TaskInfo

	pendingSorted *util.PriorityQueue
}

func compareTaskPriority(l, r interface{}) bool {
	lv := l.(*cache.TaskInfo)
	rv := r.(*cache.TaskInfo)

	return lv.Priority > rv.Priority
}

func newPodSetInfo(ps *cache.JobInfo, t *cache.Resource) *podSetInfo {
	psi := &podSetInfo{
		podSet:                 ps,
		allocated:              ps.Allocated.Clone(),
		total:                  t,
		dominantResource:       v1.ResourceCPU,
		unacceptedAllocated:    cache.EmptyResource(),
		unacceptedAssignedPods: make([]*cache.TaskInfo, 0),
		pendingSorted:          util.NewPriorityQueue(compareTaskPriority),
	}

	// Calculates the dominant resource.
	for _, rn := range cache.ResourceNames() {
		if psi.total.IsZero(rn) {
			continue
		}

		p := psi.calculateShare(rn)
		if p > psi.share {
			psi.share = p
			psi.dominantResource = rn
		}
	}

	// TODO(jinzhejz): it is better to move sorted pods to PodSet
	for _, ps := range psi.podSet.Pending {
		psi.pendingSorted.Push(ps)
	}

	glog.V(3).Infof("PodSet <%v/%v>: priority <%f>, dominant resource <%v>",
		psi.podSet.Namespace, psi.podSet.Name, psi.share, psi.dominantResource)

	return psi
}

func (psi *podSetInfo) assignPendingPod(p *cache.TaskInfo, nodeName string) {
	// assign node to pending pod temporarily
	psi.unacceptedAllocated.Add(p.Resreq)
	p.NodeName = nodeName
	psi.unacceptedAssignedPods = append(psi.unacceptedAssignedPods, p)

	glog.V(3).Infof("PodSet <%v/%v> after assignment: priority <%f>, dominant resource <%v>",
		psi.podSet.Namespace, psi.podSet.Name, psi.share, psi.dominantResource)
}

func (psi *podSetInfo) popPendingPod() *cache.TaskInfo {
	if psi.pendingSorted.Empty() {
		return nil
	}

	pi := psi.pendingSorted.Pop().(*cache.TaskInfo)

	return pi
}

func (psi *podSetInfo) pushPendingPod(p *cache.TaskInfo) {
	psi.pendingSorted.Push(p)
}

func (psi *podSetInfo) insufficientMinAvailable() int {
	insufficient := 0
	if len(psi.podSet.Running)+len(psi.podSet.Assigned) < psi.podSet.MinAvailable {
		insufficient = psi.podSet.MinAvailable - len(psi.podSet.Running) - len(psi.podSet.Assigned)
	}
	return insufficient
}

func (psi *podSetInfo) acceptAssignedPods() {
	if len(psi.unacceptedAssignedPods) == 0 {
		return
	}

	// accept temporary assigned Pods
	// put them to PodSet assigned queue
	psi.podSet.Assigned = append(psi.podSet.Assigned, psi.unacceptedAssignedPods...)
	psi.unacceptedAssignedPods = make([]*cache.TaskInfo, 0)

	// update allocate resource for consistent
	psi.allocated.Add(psi.unacceptedAllocated)
	psi.unacceptedAllocated = cache.EmptyResource()

	// update podset share
	psi.share = psi.calculateShare(psi.dominantResource)
}

func (psi *podSetInfo) discardAssignedPods() {
	if len(psi.unacceptedAssignedPods) == 0 {
		return
	}

	// clean assigned node
	for _, p := range psi.unacceptedAssignedPods {
		p.NodeName = ""
	}

	// discard temporary assigned Pods
	// put them back to PodSet pending queue
	for _, p := range psi.unacceptedAssignedPods {
		psi.pendingSorted.Push(p)
	}
	psi.unacceptedAssignedPods = make([]*cache.TaskInfo, 0)

	psi.unacceptedAllocated = cache.EmptyResource()
}

func (psi *podSetInfo) calculateShare(rn v1.ResourceName) float64 {
	return psi.allocated.Get(rn) / psi.total.Get(rn)
}
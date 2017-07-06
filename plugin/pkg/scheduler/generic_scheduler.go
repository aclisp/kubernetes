/*
Copyright 2014 The Kubernetes Authors.

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

package scheduler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang/glog"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/unversioned"
	"k8s.io/kubernetes/pkg/apis/extensions"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/util"
	"k8s.io/kubernetes/pkg/util/errors"
	"k8s.io/kubernetes/pkg/util/workqueue"
	"k8s.io/kubernetes/plugin/pkg/scheduler/algorithm"
	"k8s.io/kubernetes/plugin/pkg/scheduler/algorithm/predicates"
	schedulerapi "k8s.io/kubernetes/plugin/pkg/scheduler/api"
	"k8s.io/kubernetes/plugin/pkg/scheduler/schedulercache"
)

const (
	FridaySchedulerAnnotationKey = "scheduler.friday.yy.com/spec"
)

type FailedPredicateMap map[string][]algorithm.PredicateFailureReason

type FitError struct {
	Pod              *api.Pod
	FailedPredicates FailedPredicateMap
}

var ErrNoNodesAvailable = fmt.Errorf("no nodes available to schedule pods")

// Error returns detailed information of why the pod failed to fit on each node
func (f *FitError) Error() string {
	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("pod (%s) failed to fit in any node\n", f.Pod.Name))
	for node, predicates := range f.FailedPredicates {
		reasons := make([]string, 0)
		for _, pred := range predicates {
			reasons = append(reasons, pred.GetReason())
		}
		reasonMsg := fmt.Sprintf("fit failure on node (%s): %s\n", node, strings.Join(reasons, ", "))
		buf.WriteString(reasonMsg)
	}
	return buf.String()
}

type genericScheduler struct {
	cache             schedulercache.Cache
	predicates        map[string]algorithm.FitPredicate
	prioritizers      []algorithm.PriorityConfig
	extenders         []algorithm.SchedulerExtender
	pods              algorithm.PodLister
	replicaSetLister  algorithm.ReplicaSetLister
	lastNodeIndexLock sync.Mutex
	lastNodeIndex     uint64

	cachedNodeInfoMap map[string]*schedulercache.NodeInfo
}

// Schedule tries to schedule the given pod to one of node in the node list.
// If it succeeds, it will return the name of the node.
// If it fails, it will return a Fiterror error with reasons.
func (g *genericScheduler) Schedule(pod *api.Pod, nodeLister algorithm.NodeLister) (string, error) {
	var trace *util.Trace
	if pod != nil {
		trace = util.NewTrace(fmt.Sprintf("Scheduling %s/%s", pod.Namespace, pod.Name))
	} else {
		trace = util.NewTrace("Scheduling <nil> pod")
	}
	defer trace.LogIfLong(20 * time.Millisecond)

	nodes, err := nodeLister.List()
	if err != nil {
		return "", err
	}
	if len(nodes) == 0 {
		return "", ErrNoNodesAvailable
	}

	// Used for all fit and priority funcs.
	err = g.cache.UpdateNodeNameToInfoMap(g.cachedNodeInfoMap)
	if err != nil {
		return "", err
	}

	trace.Step("Computing predicates")
	filteredNodes, failedPredicateMap, err := findNodesThatFit(pod, g.cachedNodeInfoMap, nodes, g.predicates, g.extenders)
	if err != nil {
		return "", err
	}

	if len(filteredNodes) == 0 {
		return "", &FitError{
			Pod:              pod,
			FailedPredicates: failedPredicateMap,
		}
	}

	if _, present := pod.Annotations[FridaySchedulerAnnotationKey]; present {
		glog.V(4).Infof("Pod %v/%v is scheduling by FridayOS", pod.Namespace, pod.Name)
		trace.Step("Scheduling by FridayOS")
		return fridaySchedule(pod, g.replicaSetLister, g.cachedNodeInfoMap, filteredNodes)
	}

	glog.V(4).Infof("Pod %v/%v is scheduling by Kubernetes", pod.Namespace, pod.Name)
	trace.Step("Prioritizing")
	priorityList, err := PrioritizeNodes(pod, g.cachedNodeInfoMap, g.prioritizers, filteredNodes, g.extenders)
	if err != nil {
		return "", err
	}

	trace.Step("Selecting host")
	return g.selectHost(priorityList)
}

// selectHost takes a prioritized list of nodes and then picks one
// in a round-robin manner from the nodes that had the highest score.
func (g *genericScheduler) selectHost(priorityList schedulerapi.HostPriorityList) (string, error) {
	if len(priorityList) == 0 {
		return "", fmt.Errorf("empty priorityList")
	}

	sort.Sort(sort.Reverse(priorityList))
	maxScore := priorityList[0].Score
	firstAfterMaxScore := sort.Search(len(priorityList), func(i int) bool { return priorityList[i].Score < maxScore })

	g.lastNodeIndexLock.Lock()
	ix := int(g.lastNodeIndex % uint64(firstAfterMaxScore))
	g.lastNodeIndex++
	g.lastNodeIndexLock.Unlock()

	return priorityList[ix].Host, nil
}

// Filters the nodes to find the ones that fit based on the given predicate functions
// Each node is passed through the predicate functions to determine if it is a fit
func findNodesThatFit(
	pod *api.Pod,
	nodeNameToInfo map[string]*schedulercache.NodeInfo,
	nodes []*api.Node,
	predicateFuncs map[string]algorithm.FitPredicate,
	extenders []algorithm.SchedulerExtender) ([]*api.Node, FailedPredicateMap, error) {
	var filtered []*api.Node
	failedPredicateMap := FailedPredicateMap{}

	if len(predicateFuncs) == 0 {
		filtered = nodes
	} else {
		// Create filtered list with enough space to avoid growing it
		// and allow assigning.
		filtered = make([]*api.Node, len(nodes))
		meta := predicates.PredicateMetadata(pod, nodeNameToInfo)
		errs := []error{}

		var predicateResultLock sync.Mutex
		var filteredLen int32
		checkNode := func(i int) {
			nodeName := nodes[i].Name
			fits, failedPredicates, err := podFitsOnNode(pod, meta, nodeNameToInfo[nodeName], predicateFuncs)
			if err != nil {
				predicateResultLock.Lock()
				errs = append(errs, err)
				predicateResultLock.Unlock()
				return
			}
			if fits {
				filtered[atomic.AddInt32(&filteredLen, 1)-1] = nodes[i]
			} else {
				predicateResultLock.Lock()
				failedPredicateMap[nodeName] = failedPredicates
				predicateResultLock.Unlock()
			}
		}
		workqueue.Parallelize(16, len(nodes), checkNode)
		filtered = filtered[:filteredLen]
		if len(errs) > 0 {
			return []*api.Node{}, FailedPredicateMap{}, errors.NewAggregate(errs)
		}
	}

	if len(filtered) > 0 && len(extenders) != 0 {
		for _, extender := range extenders {
			filteredList, failedMap, err := extender.Filter(pod, filtered)
			if err != nil {
				return []*api.Node{}, FailedPredicateMap{}, err
			}

			for failedNodeName, failedMsg := range failedMap {
				if _, found := failedPredicateMap[failedNodeName]; !found {
					failedPredicateMap[failedNodeName] = []algorithm.PredicateFailureReason{}
				}
				failedPredicateMap[failedNodeName] = append(failedPredicateMap[failedNodeName], predicates.NewFailureReason(failedMsg))
			}
			filtered = filteredList
			if len(filtered) == 0 {
				break
			}
		}
	}
	return filtered, failedPredicateMap, nil
}

// Checks whether node with a given name and NodeInfo satisfies all predicateFuncs.
func podFitsOnNode(pod *api.Pod, meta interface{}, info *schedulercache.NodeInfo, predicateFuncs map[string]algorithm.FitPredicate) (bool, []algorithm.PredicateFailureReason, error) {
	var failedPredicates []algorithm.PredicateFailureReason
	for _, predicate := range predicateFuncs {
		fit, reasons, err := predicate(pod, meta, info)
		if err != nil {
			err := fmt.Errorf("SchedulerPredicates failed due to %v, which is unexpected.", err)
			return false, []algorithm.PredicateFailureReason{}, err
		}
		if !fit {
			failedPredicates = append(failedPredicates, reasons...)
		}
	}
	return len(failedPredicates) == 0, failedPredicates, nil
}

// Prioritizes the nodes by running the individual priority functions in parallel.
// Each priority function is expected to set a score of 0-10
// 0 is the lowest priority score (least preferred node) and 10 is the highest
// Each priority function can also have its own weight
// The node scores returned by the priority function are multiplied by the weights to get weighted scores
// All scores are finally combined (added) to get the total weighted scores of all nodes
func PrioritizeNodes(
	pod *api.Pod,
	nodeNameToInfo map[string]*schedulercache.NodeInfo,
	priorityConfigs []algorithm.PriorityConfig,
	nodes []*api.Node,
	extenders []algorithm.SchedulerExtender,
) (schedulerapi.HostPriorityList, error) {
	result := make(schedulerapi.HostPriorityList, 0, len(nodeNameToInfo))

	// If no priority configs are provided, then the EqualPriority function is applied
	// This is required to generate the priority list in the required format
	if len(priorityConfigs) == 0 && len(extenders) == 0 {
		return EqualPriority(pod, nodeNameToInfo, nodes)
	}

	var (
		mu             = sync.Mutex{}
		wg             = sync.WaitGroup{}
		combinedScores = make(map[string]int, len(nodeNameToInfo))
		errs           []error
	)

	for _, priorityConfig := range priorityConfigs {
		// skip the priority function if the weight is specified as 0
		if priorityConfig.Weight == 0 {
			continue
		}

		wg.Add(1)
		go func(config algorithm.PriorityConfig) {
			defer wg.Done()
			weight := config.Weight
			priorityFunc := config.Function
			prioritizedList, err := priorityFunc(pod, nodeNameToInfo, nodes)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			for i := range prioritizedList {
				host, score := prioritizedList[i].Host, prioritizedList[i].Score
				combinedScores[host] += score * weight
			}
		}(priorityConfig)
	}
	if len(errs) != 0 {
		return schedulerapi.HostPriorityList{}, errors.NewAggregate(errs)
	}

	// wait for all go routines to finish
	wg.Wait()

	if len(extenders) != 0 && nodes != nil {
		for _, extender := range extenders {
			wg.Add(1)
			go func(ext algorithm.SchedulerExtender) {
				defer wg.Done()
				prioritizedList, weight, err := ext.Prioritize(pod, nodes)
				if err != nil {
					// Prioritization errors from extender can be ignored, let k8s/other extenders determine the priorities
					return
				}
				mu.Lock()
				for i := range *prioritizedList {
					host, score := (*prioritizedList)[i].Host, (*prioritizedList)[i].Score
					combinedScores[host] += score * weight
				}
				mu.Unlock()
			}(extender)
		}
	}
	// wait for all go routines to finish
	wg.Wait()

	for host, score := range combinedScores {
		glog.V(10).Infof("Host %s Score %d", host, score)
		result = append(result, schedulerapi.HostPriority{Host: host, Score: score})
	}
	return result, nil
}

// EqualPriority is a prioritizer function that gives an equal weight of one to all nodes
func EqualPriority(_ *api.Pod, nodeNameToInfo map[string]*schedulercache.NodeInfo, nodes []*api.Node) (schedulerapi.HostPriorityList, error) {
	result := make(schedulerapi.HostPriorityList, len(nodes))
	for _, node := range nodes {
		result = append(result, schedulerapi.HostPriority{
			Host:  node.Name,
			Score: 1,
		})
	}
	return result, nil
}

func NewGenericScheduler(cache schedulercache.Cache, predicates map[string]algorithm.FitPredicate, prioritizers []algorithm.PriorityConfig, extenders []algorithm.SchedulerExtender, replicaSetLister algorithm.ReplicaSetLister) algorithm.ScheduleAlgorithm {
	return &genericScheduler{
		cache:             cache,
		predicates:        predicates,
		prioritizers:      prioritizers,
		extenders:         extenders,
		cachedNodeInfoMap: make(map[string]*schedulercache.NodeInfo),
		replicaSetLister:  replicaSetLister,
	}
}

type fridaySchedulerSpec struct {
	CountPerNode int
}

// fridaySchedule put a number of pods in the same ReplicaSet on all nodes.
// Currently it uses a binpack strategy, so that the number is fixed on each node.
func fridaySchedule(pod *api.Pod, replicaSetLister algorithm.ReplicaSetLister, nodeNameToInfo map[string]*schedulercache.NodeInfo, nodes []*api.Node) (string, error) {
	// Considering pods belonging to the same ReplicaSet
	spec := fridaySchedulerSpec{}
	selectors := make([]labels.Selector, 0, 1)
	if rss, err := replicaSetLister.GetPodReplicaSets(pod); err == nil {
		for _, rs := range rss {
			if selector, err := unversioned.LabelSelectorAsSelector(rs.Spec.Selector); err == nil {
				if err := getSchedulerSpec(rs, &spec); err == nil {
					selectors = append(selectors, selector)
				} else {
					return "", err
				}
			}
		}
	}
	glog.V(4).Infof("fridaySchedulerSpec is %+v, selectors are %v", spec, selectors)

	// Count similar pods by node
	countsByNodeName := make(map[string]int, len(nodes))
	countsByNodeNameLock := sync.Mutex{}
	maxCountByNodeName := -1
	maxCountNodeName := ""
	minCountByNodeName := -1
	minCountNodeName := ""
	nodeNamesByCount := make(map[int][]string, spec.CountPerNode*2+1)

	if len(selectors) > 0 {
		processNodeFunc := func(i int) {
			nodeName := nodes[i].Name
			count := 0
			for _, nodePod := range nodeNameToInfo[nodeName].Pods() {
				if pod.Namespace != nodePod.Namespace {
					continue
				}
				// When we are replacing a failed pod, we often see the previous
				// deleted version while scheduling the replacement.
				// Ignore the previous deleted version for spreading purposes
				// (it can still be considered for resource restrictions etc.)
				if nodePod.DeletionTimestamp != nil {
					glog.V(4).Infof("skipping pending-deleted pod: %s/%s", nodePod.Namespace, nodePod.Name)
					continue
				}
				matches := false
				for _, selector := range selectors {
					if selector.Matches(labels.Set(nodePod.ObjectMeta.Labels)) {
						matches = true
						break
					}
				}
				if matches {
					count++
				}
			}

			countsByNodeNameLock.Lock()
			defer countsByNodeNameLock.Unlock()
			countsByNodeName[nodeName] = count
			if maxCountByNodeName < 0 {
				maxCountByNodeName = count
				maxCountNodeName = nodeName
			}
			if minCountByNodeName < 0 {
				minCountByNodeName = count
				minCountNodeName = nodeName
			}
			if count > maxCountByNodeName {
				maxCountByNodeName = count
				maxCountNodeName = nodeName
			}
			if count < minCountByNodeName {
				minCountByNodeName = count
				minCountNodeName = nodeName
			}
			names := nodeNamesByCount[count]
			names = append(names, nodeName)
			nodeNamesByCount[count] = names
		}
		workqueue.Parallelize(16, len(nodes), processNodeFunc)
	}
	glog.V(4).Infof("countsByNodeName %v", countsByNodeName)
	glog.V(4).Infof("max %d on %s, min %d on %s", maxCountByNodeName, maxCountNodeName, minCountByNodeName, minCountNodeName)

	if minCountByNodeName < 0 || maxCountByNodeName < 0 || minCountByNodeName > maxCountByNodeName {
		return "", fmt.Errorf("Invalid minCountByNodeName(%d) and maxCountByNodeName(%d)", minCountByNodeName, maxCountByNodeName)
	}
	for i := minCountByNodeName; i <= maxCountByNodeName; i++ {
		if names, ok := nodeNamesByCount[i]; ok && len(names) > 0 {
			glog.V(4).Infof("nodeNamesByCount %d %v", i, names)
			if i < spec.CountPerNode {
				return names[0], nil
			}
		}
	}
	return "", fmt.Errorf("All %d nodes have %d pods each for selectors %v", len(nodes), spec.CountPerNode, selectors)
}

func getSchedulerSpec(rs extensions.ReplicaSet, spec *fridaySchedulerSpec) error {
	str, ok := rs.Annotations[FridaySchedulerAnnotationKey]
	if !ok {
		return fmt.Errorf("Not found FridaySchedulerAnnotationKey(%s) for ReplicaSet %v/%v", FridaySchedulerAnnotationKey, rs.Namespace, rs.Name)
	}
	s := fridaySchedulerSpec{}
	if err := json.Unmarshal([]byte(str), &s); err != nil {
		return fmt.Errorf("Invalid format of FridaySchedulerAnnotationKey(%s) for ReplicaSet %v/%v", FridaySchedulerAnnotationKey, rs.Namespace, rs.Name)
	}
	if s.CountPerNode <= 0 {
		return fmt.Errorf("CountPerNode(%d) in FridaySchedulerAnnotationKey(%s) must greater than zero for ReplicaSet %v/%v", s.CountPerNode, FridaySchedulerAnnotationKey, rs.Namespace, rs.Name)
	}
	// Try to overwrite the input spec if and only if it is not assigned.
	if spec.CountPerNode == 0 {
		spec.CountPerNode = s.CountPerNode
	} else if spec.CountPerNode != s.CountPerNode {
		return fmt.Errorf("CountPerNode(%d) in FridaySchedulerAnnotationKey(%s) of ReplicaSet %v/%v is different from previous assigned CountPerNode(%d)", s.CountPerNode, FridaySchedulerAnnotationKey, rs.Namespace, rs.Name, spec.CountPerNode)
	}
	return nil
}

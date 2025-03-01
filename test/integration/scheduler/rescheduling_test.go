/*
Copyright 2022 The Kubernetes Authors.

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
	"context"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubernetes/pkg/scheduler"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	st "k8s.io/kubernetes/pkg/scheduler/testing"
	testutils "k8s.io/kubernetes/test/integration/util"
)

var _ framework.PermitPlugin = &PermitPlugin{}
var _ framework.EnqueueExtensions = &PermitPlugin{}

type PermitPlugin struct {
	name            string
	statusCode      framework.Code
	numPermitCalled int
}

func (pp *PermitPlugin) Name() string {
	return pp.name
}

func (pp *PermitPlugin) Permit(ctx context.Context, state *framework.CycleState, p *v1.Pod, nodeName string) (*framework.Status, time.Duration) {
	pp.numPermitCalled += 1

	if pp.statusCode == framework.Error {
		return framework.NewStatus(framework.Error, "failed to permit"), 0
	}

	if pp.statusCode == framework.Unschedulable {
		if pp.numPermitCalled <= 1 {
			return framework.NewStatus(framework.Unschedulable, "reject to permit"), 0
		}
	}

	return nil, 0
}

func (pp *PermitPlugin) EventsToRegister() []framework.ClusterEventWithHint {
	return []framework.ClusterEventWithHint{
		{
			Event: framework.ClusterEvent{Resource: framework.Node, ActionType: framework.Add},
			QueueingHintFn: func(pod *v1.Pod, oldObj, newObj interface{}) framework.QueueingHint {
				return framework.QueueImmediately
			},
		},
	}
}

func TestReScheduling(t *testing.T) {
	testContext := testutils.InitTestAPIServer(t, "permit-plugin", nil)
	tests := []struct {
		name    string
		plugins []framework.Plugin
		action  func() error
		// The first time for pod scheduling, we make pod scheduled error or unschedulable on purpose.
		// This is controlled by wantFirstSchedulingError. By default, pod is unschedulable.
		wantFirstSchedulingError bool

		// wantScheduled/wantError means the final expected scheduling result.
		wantScheduled bool
		wantError     bool
	}{
		{
			name: "Rescheduling pod rejected by Permit Plugin",
			plugins: []framework.Plugin{
				&PermitPlugin{name: "permit", statusCode: framework.Unschedulable},
			},
			action: func() error {
				_, err := createNode(testContext.ClientSet, st.MakeNode().Name("fake-node").Obj())
				return err
			},
			wantScheduled: true,
		},
		{
			name: "Rescheduling pod rejected by Permit Plugin with unrelated event",
			plugins: []framework.Plugin{
				&PermitPlugin{name: "permit", statusCode: framework.Unschedulable},
			},
			action: func() error {
				_, err := createPausePod(testContext.ClientSet,
					initPausePod(&testutils.PausePodConfig{Name: "test-pod-2", Namespace: testContext.NS.Name}))
				return err
			},
			wantScheduled: false,
		},
		{
			name: "Rescheduling pod failed by Permit Plugin",
			plugins: []framework.Plugin{
				&PermitPlugin{name: "permit", statusCode: framework.Error},
			},
			action: func() error {
				_, err := createNode(testContext.ClientSet, st.MakeNode().Name("fake-node").Obj())
				return err
			},
			wantFirstSchedulingError: true,
			wantError:                true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Create a plugin registry for testing. Register only a permit plugin.
			registry, prof := InitRegistryAndConfig(t, nil, test.plugins...)

			testCtx, teardown := InitTestSchedulerForFrameworkTest(t, testContext, 2,
				scheduler.WithProfiles(prof),
				scheduler.WithFrameworkOutOfTreeRegistry(registry))
			defer teardown()

			pod, err := createPausePod(testCtx.ClientSet,
				initPausePod(&testutils.PausePodConfig{Name: "test-pod", Namespace: testCtx.NS.Name}))
			if err != nil {
				t.Errorf("Error while creating a test pod: %v", err)
			}

			// The first time for scheduling, pod is error or unschedulable, controlled by wantFirstSchedulingError
			if test.wantFirstSchedulingError {
				if err = wait.Poll(10*time.Millisecond, 30*time.Second, testutils.PodSchedulingError(testCtx.ClientSet, pod.Namespace, pod.Name)); err != nil {
					t.Errorf("Expected a scheduling error, but got: %v", err)
				}
			} else {
				if err = waitForPodUnschedulable(testCtx.ClientSet, pod); err != nil {
					t.Errorf("Didn't expect the pod to be scheduled. error: %v", err)
				}
			}

			if test.action() != nil {
				if err = test.action(); err != nil {
					t.Errorf("Perform action() error: %v", err)
				}
			}

			if test.wantScheduled {
				if err = testutils.WaitForPodToSchedule(testCtx.ClientSet, pod); err != nil {
					t.Errorf("Didn't expect the pod to be unschedulable. error: %v", err)
				}
			} else if test.wantError {
				if err = wait.Poll(10*time.Millisecond, 30*time.Second, testutils.PodSchedulingError(testCtx.ClientSet, pod.Namespace, pod.Name)); err != nil {
					t.Errorf("Expected a scheduling error, but got: %v", err)
				}
			} else {
				if err = waitForPodUnschedulable(testCtx.ClientSet, pod); err != nil {
					t.Errorf("Didn't expect the pod to be scheduled. error: %v", err)
				}
			}
		})
	}
}

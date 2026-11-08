/*
Copyright 2026 Dmitry Stepanov.

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

package v1alpha1

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	chaosv1alpha1 "github.com/bibigon14/chaos-scheduler-operator/api/v1alpha1"
)

// validExperiment returns a ChaosExperiment that satisfies every rule.
// Tests mutate one field at a time and assert the webhook catches that
// one thing - keeps the matrix readable and the failure cause obvious.
func validExperiment() *chaosv1alpha1.ChaosExperiment {
	return &chaosv1alpha1.ChaosExperiment{
		ObjectMeta: metav1.ObjectMeta{Name: "exp", Namespace: "apps"},
		Spec: chaosv1alpha1.ChaosExperimentSpec{
			Schedule: "0 */2 * * *",
			Selector: chaosv1alpha1.PodSelector{
				Namespace: "apps",
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"chaos.dstepanov.dev/eligible": "true",
					},
				},
			},
			Action: chaosv1alpha1.Action{
				Type:    chaosv1alpha1.ActionTypePodKill,
				PodKill: &chaosv1alpha1.PodKillSpec{Count: 1},
			},
		},
	}
}

func TestValidateCreate(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(e *chaosv1alpha1.ChaosExperiment)
		wantErr   bool
		wantMatch string // substring assertion on the aggregated error
	}{
		{
			name:    "happy path passes",
			mutate:  func(_ *chaosv1alpha1.ChaosExperiment) {},
			wantErr: false,
		},
		{
			name: "schedule with out-of-range hour is rejected",
			mutate: func(e *chaosv1alpha1.ChaosExperiment) {
				e.Spec.Schedule = "0 24 * * *"
			},
			wantErr:   true,
			wantMatch: "spec.schedule",
		},
		{
			name: "schedule nonsense is rejected",
			mutate: func(e *chaosv1alpha1.ChaosExperiment) {
				e.Spec.Schedule = "every tuesday"
			},
			wantErr:   true,
			wantMatch: "spec.schedule",
		},
		{
			name: "reserved namespace kube-system is rejected",
			mutate: func(e *chaosv1alpha1.ChaosExperiment) {
				e.Spec.Selector.Namespace = "kube-system"
			},
			wantErr:   true,
			wantMatch: "reserved",
		},
		{
			name: "reserved namespace kube-public is rejected",
			mutate: func(e *chaosv1alpha1.ChaosExperiment) {
				e.Spec.Selector.Namespace = "kube-public"
			},
			wantErr:   true,
			wantMatch: "reserved",
		},
		{
			name: "reserved namespace kube-node-lease is rejected",
			mutate: func(e *chaosv1alpha1.ChaosExperiment) {
				e.Spec.Selector.Namespace = "kube-node-lease"
			},
			wantErr:   true,
			wantMatch: "reserved",
		},
		{
			name: "application namespace is allowed",
			mutate: func(e *chaosv1alpha1.ChaosExperiment) {
				e.Spec.Selector.Namespace = "blue-green-demo"
			},
			wantErr: false,
		},
		{
			name: "nil labelSelector is rejected",
			mutate: func(e *chaosv1alpha1.ChaosExperiment) {
				e.Spec.Selector.LabelSelector = nil
			},
			wantErr:   true,
			wantMatch: "every pod",
		},
		{
			name: "empty labelSelector is rejected",
			mutate: func(e *chaosv1alpha1.ChaosExperiment) {
				e.Spec.Selector.LabelSelector = &metav1.LabelSelector{}
			},
			wantErr:   true,
			wantMatch: "every pod",
		},
		{
			name: "labelSelector with only matchExpressions is allowed",
			mutate: func(e *chaosv1alpha1.ChaosExperiment) {
				e.Spec.Selector.LabelSelector = &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: "app", Operator: metav1.LabelSelectorOpExists},
					},
				}
			},
			wantErr: false,
		},
		{
			name: "missing podKill with action=PodKill is rejected",
			mutate: func(e *chaosv1alpha1.ChaosExperiment) {
				e.Spec.Action.PodKill = nil
			},
			wantErr:   true,
			wantMatch: "podKill is required",
		},
		{
			name: "podKill count zero is rejected",
			mutate: func(e *chaosv1alpha1.ChaosExperiment) {
				e.Spec.Action.PodKill.Count = 0
			},
			wantErr:   true,
			wantMatch: ">= 1",
		},
		{
			name: "podKill count negative is rejected",
			mutate: func(e *chaosv1alpha1.ChaosExperiment) {
				e.Spec.Action.PodKill.Count = -3
			},
			wantErr:   true,
			wantMatch: ">= 1",
		},
	}

	v := &ChaosExperimentCustomValidator{}
	ctx := context.Background()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exp := validExperiment()
			tc.mutate(exp)

			_, err := v.ValidateCreate(ctx, exp)

			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("expected error, got nil")
			case !tc.wantErr && err != nil:
				t.Fatalf("expected no error, got %v", err)
			case tc.wantErr && tc.wantMatch != "" && !strings.Contains(err.Error(), tc.wantMatch):
				t.Fatalf("expected error to contain %q, got %v", tc.wantMatch, err)
			}
		})
	}
}

// TestValidateCreateAggregatesErrors confirms a multi-field violation
// surfaces every broken rule in one admission response, so the operator
// can fix them in one edit instead of discovering the next one by
// kubectl apply trial-and-error.
func TestValidateCreateAggregatesErrors(t *testing.T) {
	exp := validExperiment()
	exp.Spec.Schedule = "bogus"
	exp.Spec.Selector.Namespace = "kube-system"
	exp.Spec.Selector.LabelSelector = nil

	v := &ChaosExperimentCustomValidator{}
	_, err := v.ValidateCreate(context.Background(), exp)
	if err == nil {
		t.Fatal("expected aggregated error, got nil")
	}

	for _, needle := range []string{
		"spec.schedule",
		"spec.selector.namespace",
		"spec.selector.labelSelector",
	} {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("expected aggregated error to mention %q, got %v", needle, err)
		}
	}
}

// TestValidateUpdate confirms the same rules apply to updates. A smoke
// test is enough because the underlying validator is shared with Create.
func TestValidateUpdate(t *testing.T) {
	v := &ChaosExperimentCustomValidator{}

	oldObj := validExperiment()
	newObj := validExperiment()
	newObj.Spec.Schedule = "bogus"

	if _, err := v.ValidateUpdate(context.Background(), oldObj, newObj); err == nil {
		t.Fatal("expected error for malformed schedule on update, got nil")
	}
}

// TestValidateDelete confirms deletion is always allowed. Removing a CR
// must work even if the admission rules have tightened since it was
// created, otherwise cleanup becomes impossible.
func TestValidateDelete(t *testing.T) {
	v := &ChaosExperimentCustomValidator{}
	if _, err := v.ValidateDelete(context.Background(), validExperiment()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

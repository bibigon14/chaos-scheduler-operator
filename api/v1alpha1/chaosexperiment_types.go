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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ChaosExperimentSpec defines the desired state of ChaosExperiment.
type ChaosExperimentSpec struct {
	// Schedule is a standard 5-field cron expression (min hour dom mon dow),
	// evaluated in the controller's local time zone.
	// +kubebuilder:validation:MinLength=9
	Schedule string `json:"schedule"`

	// Selector picks target pods for the action. Both fields are required to
	// prevent an empty selector matching every pod in the namespace.
	Selector PodSelector `json:"selector"`

	// Action describes what to do when the experiment fires.
	Action Action `json:"action"`

	// Guardrail aborts the run when a Prometheus signal crosses a threshold.
	// Absence means no guardrail is evaluated.
	// +optional
	Guardrail *Guardrail `json:"guardrail,omitempty"`

	// TTL bounds the duration of a single experiment run.
	// +optional
	// +kubebuilder:default="5m"
	TTL metav1.Duration `json:"ttl,omitempty"`

	// Suspend halts scheduling without deleting the resource.
	// +optional
	// +kubebuilder:default=false
	Suspend bool `json:"suspend,omitempty"`
}

// PodSelector describes how to pick target pods.
type PodSelector struct {
	// Namespace to target. Must not be empty; kube-system and other reserved
	// namespaces are rejected by the validating webhook.
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// LabelSelector filters pods within Namespace. Required; the webhook
	// rejects a selector with no matchLabels and no matchExpressions.
	LabelSelector *metav1.LabelSelector `json:"labelSelector"`
}

// ActionType enumerates supported chaos actions.
// +kubebuilder:validation:Enum=PodKill
type ActionType string

const (
	ActionTypePodKill ActionType = "PodKill"
)

// Action describes what the reconciler does to selected pods.
type Action struct {
	Type ActionType `json:"type"`

	// PodKill parameters, required when Type=PodKill.
	// +optional
	PodKill *PodKillSpec `json:"podKill,omitempty"`
}

// PodKillSpec configures the PodKill action.
type PodKillSpec struct {
	// Count of pods to kill per run.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Count int32 `json:"count,omitempty"`
}

// Guardrail describes a Prometheus-based abort condition.
type Guardrail struct {
	// PrometheusURL points at a Prometheus HTTP API root (no trailing slash).
	// Example: http://prometheus.monitoring.svc:9090
	PrometheusURL string `json:"prometheusUrl"`

	// Query is any PromQL that returns a scalar or an instant vector with
	// exactly one sample. Anything else fails the run with LastResult=Failed.
	Query string `json:"query"`

	// AbortIfGreaterThan is the exclusive upper bound. If the query value is
	// strictly greater, the run is aborted before the action executes.
	AbortIfGreaterThan resource.Quantity `json:"abortIfGreaterThan"`
}

// ExperimentResult enumerates outcomes of a single reconcile-triggered run.
// +kubebuilder:validation:Enum=Completed;Aborted;Failed;Skipped
type ExperimentResult string

const (
	ResultCompleted ExperimentResult = "Completed"
	ResultAborted   ExperimentResult = "Aborted"
	ResultFailed    ExperimentResult = "Failed"
	ResultSkipped   ExperimentResult = "Skipped"
)

// ChaosExperimentStatus is the last-observed state of the resource.
type ChaosExperimentStatus struct {
	// LastRun is when the reconciler last attempted an action.
	// +optional
	LastRun *metav1.Time `json:"lastRun,omitempty"`

	// LastResult is the outcome of the last run.
	// +optional
	LastResult ExperimentResult `json:"lastResult,omitempty"`

	// Reason is human-readable context for LastResult, e.g. the guardrail
	// value that triggered an abort.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Runs is the cumulative count of Completed, Aborted, and Failed runs.
	// Skipped runs (Suspend=true, no due time) are not counted.
	// +optional
	Runs int64 `json:"runs,omitempty"`

	// NextRun is the next scheduled fire time computed from Spec.Schedule.
	// +optional
	NextRun *metav1.Time `json:"nextRun,omitempty"`

	// Conditions holds standard status conditions for the resource.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=chaos
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Last Run",type=date,JSONPath=`.status.lastRun`
// +kubebuilder:printcolumn:name="Result",type=string,JSONPath=`.status.lastResult`
// +kubebuilder:printcolumn:name="Runs",type=integer,JSONPath=`.status.runs`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ChaosExperiment is a scheduled fault injection with an SLO guardrail.
type ChaosExperiment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ChaosExperimentSpec   `json:"spec,omitempty"`
	Status ChaosExperimentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ChaosExperimentList contains a list of ChaosExperiment.
type ChaosExperimentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ChaosExperiment `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &ChaosExperiment{}, &ChaosExperimentList{})
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})
}

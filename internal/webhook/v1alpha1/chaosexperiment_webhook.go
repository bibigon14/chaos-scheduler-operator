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

// Package v1alpha1 implements the validating admission webhook for
// ChaosExperiment resources. Four rules are enforced on every create
// and update:
//
//  1. spec.schedule must parse as a 5-field cron expression.
//  2. spec.selector.namespace must not be one of the Kubernetes-reserved
//     control-plane namespaces (kube-system, kube-public, kube-node-lease).
//  3. spec.selector.labelSelector must select a non-empty subset. An empty
//     selector matches every pod in the namespace, which turns the operator
//     into a scheduled namespace-wipe.
//  4. For action.type=PodKill, action.podKill.count must be >= 1.
//
// Delete is always permitted - an operator should be able to remove a CR
// even if the admission rules have tightened since it was created.
//
// The validator aggregates errors instead of short-circuiting on the first
// failure, so an edit that breaks several rules at once surfaces all of
// them in a single admission response.
package v1alpha1

import (
	"context"
	"fmt"

	"github.com/robfig/cron/v3"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	chaosv1alpha1 "github.com/bibigon14/chaos-scheduler-operator/api/v1alpha1"
)

// reservedNamespaces are Kubernetes control-plane namespaces that must
// never be targeted by a chaos experiment. Killing pods in these breaks
// the cluster, not an SLO budget - the resulting signal is unusable.
var reservedNamespaces = map[string]struct{}{
	"kube-system":     {},
	"kube-public":     {},
	"kube-node-lease": {},
}

// cronParser mirrors the one in internal/controller. Validating here
// rejects a bad schedule at admission time instead of letting the CR
// land and surface as a Failed run on the next reconcile.
var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
)

// log is the webhook's package-level logger.
var chaoslog = logf.Log.WithName("chaosexperiment-webhook")

// SetupChaosExperimentWebhookWithManager registers the validator with the
// controller-runtime manager. Called from cmd/main.go after the primary
// reconciler is wired.
func SetupChaosExperimentWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&chaosv1alpha1.ChaosExperiment{}).
		WithValidator(&ChaosExperimentCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-chaos-dstepanov-dev-v1alpha1-chaosexperiment,mutating=false,failurePolicy=fail,sideEffects=None,groups=chaos.dstepanov.dev,resources=chaosexperiments,verbs=create;update,versions=v1alpha1,name=vchaosexperiment-v1alpha1.kb.io,admissionReviewVersions=v1

// ChaosExperimentCustomValidator implements webhook.CustomValidator.
type ChaosExperimentCustomValidator struct{}

var _ webhook.CustomValidator = &ChaosExperimentCustomValidator{}

// ValidateCreate is invoked on CREATE admission requests.
func (v *ChaosExperimentCustomValidator) ValidateCreate(
	_ context.Context, obj runtime.Object,
) (admission.Warnings, error) {
	exp, ok := obj.(*chaosv1alpha1.ChaosExperiment)
	if !ok {
		return nil, fmt.Errorf("expected ChaosExperiment, got %T", obj)
	}
	chaoslog.V(1).Info("validate create", "name", exp.Name, "ns", exp.Namespace)
	return nil, validateChaosExperiment(exp).ToAggregate()
}

// ValidateUpdate is invoked on UPDATE admission requests. The old object
// is ignored - the rules care only about the shape of the result.
func (v *ChaosExperimentCustomValidator) ValidateUpdate(
	_ context.Context, _, newObj runtime.Object,
) (admission.Warnings, error) {
	exp, ok := newObj.(*chaosv1alpha1.ChaosExperiment)
	if !ok {
		return nil, fmt.Errorf("expected ChaosExperiment, got %T", newObj)
	}
	chaoslog.V(1).Info("validate update", "name", exp.Name, "ns", exp.Namespace)
	return nil, validateChaosExperiment(exp).ToAggregate()
}

// ValidateDelete always permits deletion. An operator should be free to
// remove a CR even if the admission rules have tightened since it was
// created, otherwise cleanup becomes impossible.
func (v *ChaosExperimentCustomValidator) ValidateDelete(
	_ context.Context, _ runtime.Object,
) (admission.Warnings, error) {
	return nil, nil
}

// validateChaosExperiment runs every rule and returns the collected
// errors. Callers pass the aggregated result back through the admission
// response so a multi-field violation is surfaced in one round-trip.
func validateChaosExperiment(exp *chaosv1alpha1.ChaosExperiment) field.ErrorList {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	// Rule 1: schedule must parse. CRD MinLength=9 rules out empty strings
	// and trivially short garbage; this catches syntactically-shaped but
	// invalid expressions such as "0 24 * * *" (hour out of range).
	if _, err := cronParser.Parse(exp.Spec.Schedule); err != nil {
		errs = append(errs, field.Invalid(
			specPath.Child("schedule"), exp.Spec.Schedule,
			fmt.Sprintf("not a valid 5-field cron expression: %v", err),
		))
	}

	// Rule 2: target namespace is not reserved.
	if _, bad := reservedNamespaces[exp.Spec.Selector.Namespace]; bad {
		errs = append(errs, field.Forbidden(
			specPath.Child("selector").Child("namespace"),
			fmt.Sprintf("target namespace %q is reserved; chaos against the control plane is not allowed",
				exp.Spec.Selector.Namespace),
		))
	}

	// Rule 3: label selector is non-empty. Either matchLabels or
	// matchExpressions is enough; both nil means "every pod".
	if exp.Spec.Selector.LabelSelector == nil ||
		(len(exp.Spec.Selector.LabelSelector.MatchLabels) == 0 &&
			len(exp.Spec.Selector.LabelSelector.MatchExpressions) == 0) {
		errs = append(errs, field.Required(
			specPath.Child("selector").Child("labelSelector"),
			"must specify matchLabels or matchExpressions - an empty selector would match every pod in the namespace",
		))
	}

	// Rule 4: PodKill is well-formed. CRD Minimum=1 and Default=1 normally
	// cover this, but a client that sends Count=0 explicitly bypasses the
	// default because zero is the Go zero-value and the server cannot tell
	// unset from "set to zero".
	if exp.Spec.Action.Type == chaosv1alpha1.ActionTypePodKill {
		switch {
		case exp.Spec.Action.PodKill == nil:
			errs = append(errs, field.Required(
				specPath.Child("action").Child("podKill"),
				"podKill is required when action.type=PodKill",
			))
		case exp.Spec.Action.PodKill.Count < 1:
			errs = append(errs, field.Invalid(
				specPath.Child("action").Child("podKill").Child("count"),
				exp.Spec.Action.PodKill.Count,
				"must be >= 1",
			))
		}
	}

	return errs
}

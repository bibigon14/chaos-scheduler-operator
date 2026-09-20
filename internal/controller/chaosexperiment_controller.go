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

package controller

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	chaosv1alpha1 "github.com/bibigon14/chaos-scheduler-operator/api/v1alpha1"
	"github.com/bibigon14/chaos-scheduler-operator/internal/metrics"
)

// cronParser accepts standard 5-field expressions. All Next() computations
// are anchored to UTC below, so schedule semantics don't shift between the
// operator's local TZ (Docker containers default to UTC, developer machines
// don't) and the CR author's mental model. Document this in the CRD when
// v1alpha2 lands.
var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
)

// promAPIFactory lets tests inject a fake Prometheus client. Production wires
// this to newHTTPPromAPI via SetupWithManager.
type promAPIFactory func(url string) (promv1.API, error)

func newHTTPPromAPI(url string) (promv1.API, error) {
	c, err := api.NewClient(api.Config{Address: url})
	if err != nil {
		return nil, err
	}
	return promv1.NewAPI(c), nil
}

// ChaosExperimentReconciler reconciles a ChaosExperiment object.
type ChaosExperimentReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	NewPromAPI   promAPIFactory
	Now          func() time.Time // seam for tests
	QueryTimeout time.Duration
}

// +kubebuilder:rbac:groups=chaos.dstepanov.dev,resources=chaosexperiments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=chaos.dstepanov.dev,resources=chaosexperiments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=chaos.dstepanov.dev,resources=chaosexperiments/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete

func (r *ChaosExperimentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	var exp chaosv1alpha1.ChaosExperiment
	if err := r.Get(ctx, req.NamespacedName, &exp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if exp.Spec.Suspend {
		logger.V(1).Info("suspended, skipping")
		return r.scheduleNext(ctx, &exp, nil)
	}

	sched, err := cronParser.Parse(exp.Spec.Schedule)
	if err != nil {
		return r.finish(ctx, &exp, chaosv1alpha1.ResultFailed,
			fmt.Sprintf("invalid schedule %q: %v", exp.Spec.Schedule, err))
	}

	now := r.Now().UTC()
	nextFire := sched.Next(now)
	lastRun := time.Time{}
	if exp.Status.LastRun != nil {
		lastRun = exp.Status.LastRun.UTC()
	}
	dueAt := sched.Next(lastRun)

	// Not yet due: requeue at the exact fire time.
	if now.Before(dueAt) {
		exp.Status.NextRun = &metav1.Time{Time: nextFire}
		if err := r.Status().Update(ctx, &exp); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return ctrl.Result{RequeueAfter: dueAt.Sub(now)}, nil
	}
	logger.Info("experiment due, executing run",
		"schedule", exp.Spec.Schedule,
		"lastRun", lastRun,
		"dueAt", dueAt)

	// Guardrail check: any error or over-threshold aborts before touching pods.
	if exp.Spec.Guardrail != nil {
		aborted, reason, err := r.checkGuardrail(ctx, &exp)
		if err != nil {
			logger.Error(err, "guardrail check errored, aborting run")
			return r.finish(ctx, &exp, chaosv1alpha1.ResultFailed,
				fmt.Sprintf("guardrail error: %v", err))
		}
		if aborted {
			logger.Info("guardrail aborted run", "reason", reason)
			return r.finish(ctx, &exp, chaosv1alpha1.ResultAborted, reason)
		}
		logger.Info("guardrail check passed")
	}

	switch exp.Spec.Action.Type {
	case chaosv1alpha1.ActionTypePodKill:
		logger.Info("executing PodKill action", "requestedCount", exp.Spec.Action.PodKill.Count)
		killed, err := r.podKill(ctx, &exp)
		if err != nil {
			logger.Error(err, "PodKill failed")
			return r.finish(ctx, &exp, chaosv1alpha1.ResultFailed,
				fmt.Sprintf("PodKill failed: %v", err))
		}
		logger.Info("PodKill completed", "killed", killed)
		return r.finish(ctx, &exp, chaosv1alpha1.ResultCompleted,
			fmt.Sprintf("killed %d pod(s)", killed))
	default:
		return r.finish(ctx, &exp, chaosv1alpha1.ResultFailed,
			fmt.Sprintf("unsupported action %q", exp.Spec.Action.Type))		
	}
}

// checkGuardrail evaluates the PromQL and returns (aborted, reason, err).
// A scalar or single-sample vector is required; anything else is an error.
func (r *ChaosExperimentReconciler) checkGuardrail(
	ctx context.Context, exp *chaosv1alpha1.ChaosExperiment,
) (bool, string, error) {
	g := exp.Spec.Guardrail
	outcome := "ok"
	start := r.Now()
	defer func() {
		metrics.GuardrailCheckDuration.
			WithLabelValues(exp.Namespace, exp.Name, outcome).
			Observe(r.Now().Sub(start).Seconds())
	}()

	papi, err := r.NewPromAPI(g.PrometheusURL)
	if err != nil {
		outcome = "client_error"
		return false, "", err
	}

	qctx, cancel := context.WithTimeout(ctx, r.QueryTimeout)
	defer cancel()

	res, _, err := papi.Query(qctx, g.Query, r.Now())
	if err != nil {
		outcome = "query_error"
		return false, "", err
	}

	value, err := extractScalar(res)
	if err != nil {
		outcome = "parse_error"
		return false, "", err
	}

	threshold := g.AbortIfGreaterThan.AsApproximateFloat64()
	if value > threshold {
		outcome = "aborted"
		return true, fmt.Sprintf("guardrail %s=%.4f exceeds threshold %.4f",
			g.Query, value, threshold), nil
	}
	return false, "", nil
}

func extractScalar(v model.Value) (float64, error) {
	switch t := v.(type) {
	case *model.Scalar:
		return float64(t.Value), nil
	case model.Vector:
		if len(t) != 1 {
			return 0, fmt.Errorf("vector must have exactly 1 sample, got %d", len(t))
		}
		return float64(t[0].Value), nil
	default:
		return 0, fmt.Errorf("unsupported result type %T", v)
	}
}

// podKill lists pods matching the selector and deletes N random ones.
// Returns the number actually deleted.
func (r *ChaosExperimentReconciler) podKill(
	ctx context.Context, exp *chaosv1alpha1.ChaosExperiment,
) (int, error) {
	sel, err := metav1.LabelSelectorAsSelector(exp.Spec.Selector.LabelSelector)
	if err != nil {
		return 0, fmt.Errorf("invalid labelSelector: %w", err)
	}

	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(exp.Spec.Selector.Namespace),
		client.MatchingLabelsSelector{Selector: sel},
	); err != nil {
		return 0, err
	}

	// Filter to Running pods with no active deletion, so we don't count
	// already-terminating ones toward the kill quota.
	eligible := pods.Items[:0]
	for _, p := range pods.Items {
		if p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning {
			eligible = append(eligible, p)
		}
	}
	if len(eligible) == 0 {
		return 0, nil
	}

	count := int(exp.Spec.Action.PodKill.Count)
	if count > len(eligible) {
		count = len(eligible)
	}
	rand.Shuffle(len(eligible), func(i, j int) { eligible[i], eligible[j] = eligible[j], eligible[i] })

	deleted := 0
	for _, p := range eligible[:count] {
		if err := r.Delete(ctx, &p); err != nil && !apierrors.IsNotFound(err) {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

// finish records the outcome, bumps metrics, and requeues at the next fire time.
func (r *ChaosExperimentReconciler) finish(
	ctx context.Context, exp *chaosv1alpha1.ChaosExperiment,
	result chaosv1alpha1.ExperimentResult, reason string,
) (ctrl.Result, error) {
	now := r.Now()
	exp.Status.LastRun = &metav1.Time{Time: now}
	exp.Status.LastResult = result
	exp.Status.Reason = reason
	exp.Status.Runs++

	metrics.RunsTotal.
		WithLabelValues(exp.Namespace, exp.Name, string(result)).
		Inc()
	metrics.LastRunTimestamp.
		WithLabelValues(exp.Namespace, exp.Name).
		Set(float64(now.Unix()))

	return r.scheduleNext(ctx, exp, &now)
}

// scheduleNext writes status and computes the next requeue delay.
func (r *ChaosExperimentReconciler) scheduleNext(
	ctx context.Context, exp *chaosv1alpha1.ChaosExperiment, from *time.Time,
) (ctrl.Result, error) {
	sched, err := cronParser.Parse(exp.Spec.Schedule)
	if err != nil {
		// Schedule was validated earlier if we got here from finish(); the
		// only remaining path is Suspend=true, where we requeue in a minute
		// to notice un-suspension.
		return ctrl.Result{RequeueAfter: time.Minute}, r.Status().Update(ctx, exp)
	}

	base := r.Now().UTC()
	if from != nil {
		base = from.UTC()
	}
	next := sched.Next(base)
	exp.Status.NextRun = &metav1.Time{Time: next}

	if err := r.Status().Update(ctx, exp); err != nil {
		if apierrors.IsConflict(err) {
			// Conflict on optimistic update: retry immediately with a small
			// backoff via RequeueAfter (Requeue field was deprecated in
			// controller-runtime v0.19).
			return ctrl.Result{RequeueAfter: 100 * time.Millisecond}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: next.Sub(r.Now())}, nil
}

func (r *ChaosExperimentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.NewPromAPI == nil {
		r.NewPromAPI = newHTTPPromAPI
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.QueryTimeout == 0 {
		r.QueryTimeout = 10 * time.Second
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&chaosv1alpha1.ChaosExperiment{}).
		Named("chaosexperiment").
		Complete(r)
}

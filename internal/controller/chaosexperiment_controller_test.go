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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	chaosv1alpha1 "github.com/bibigon14/chaos-scheduler-operator/api/v1alpha1"
)

// fakePromAPI implements just enough of promv1.API to drive tests. Only Query
// is exercised; every other method is left as a panic so a wrong assumption
// surfaces loudly instead of silently returning zero values.
type fakePromAPI struct {
	promv1.API
	result model.Value
	err    error
}

func (f *fakePromAPI) Query(_ context.Context, _ string, _ time.Time, _ ...promv1.Option) (model.Value, promv1.Warnings, error) {
	return f.result, nil, f.err
}

func newFakePromFactory(result model.Value, err error) promAPIFactory {
	return func(_ string) (promv1.API, error) {
		return &fakePromAPI{result: result, err: err}, nil
	}
}

// nsCounter guarantees each It block gets its own namespace, so parallel or
// re-run specs don't collide on the same objects in the shared envtest apiserver.
var nsCounter = 0

func newTestNamespace(ctx context.Context) string {
	nsCounter++
	name := fmt.Sprintf("chaos-test-%d-%d", time.Now().UnixNano(), nsCounter)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	return name
}

// baseExperiment returns a valid CR with cron */2 hours and a matchLabels selector.
// Callers mutate whichever field they're testing before Create.
func baseExperiment(name, ns string) *chaosv1alpha1.ChaosExperiment {
	return &chaosv1alpha1.ChaosExperiment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: chaosv1alpha1.ChaosExperimentSpec{
			Schedule: "0 */2 * * *",
			Selector: chaosv1alpha1.PodSelector{
				Namespace: ns,
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"chaos.dstepanov.dev/eligible": "true"},
				},
			},
			Action: chaosv1alpha1.Action{
				Type:    chaosv1alpha1.ActionTypePodKill,
				PodKill: &chaosv1alpha1.PodKillSpec{Count: 1},
			},
			TTL: metav1.Duration{Duration: 5 * time.Minute},
		},
	}
}

// runningPod creates a Pod already in Phase=Running via a status subresource
// update; the default Pod created via envtest is Pending and would be filtered
// out by the reconciler's eligibility check.
func runningPod(ctx context.Context, ns, name string) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{"chaos.dstepanov.dev/eligible": "true"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "pause", Image: "registry.k8s.io/pause:3.9"}},
		},
	}
	Expect(k8sClient.Create(ctx, p)).To(Succeed())
	p.Status.Phase = corev1.PodRunning
	Expect(k8sClient.Status().Update(ctx, p)).To(Succeed())
	return p
}

var _ = Describe("ChaosExperiment Controller", func() {
	var (
		ns         string
		reconciler *ChaosExperimentReconciler
		nowFunc    func() time.Time
		fixedNow   time.Time
	)

	BeforeEach(func() {
		ns = newTestNamespace(ctx)
		// A deterministic clock: put "now" comfortably past any cron fire so
		// the "due" path is deterministic. Individual specs override as needed.
		fixedNow = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
		nowFunc = func() time.Time { return fixedNow }

		reconciler = &ChaosExperimentReconciler{
			Client:       k8sClient,
			Scheme:       k8sClient.Scheme(),
			Now:          func() time.Time { return nowFunc() },
			QueryTimeout: 5 * time.Second,
			// Default: guardrail returns 0 (never aborts). Specs that care
			// about the guardrail path override NewPromAPI before Reconcile.
			NewPromAPI: newFakePromFactory(&model.Scalar{Value: 0}, nil),
		}
	})

	Context("when the experiment is suspended", func() {
		It("skips the action and requeues at the next scheduled time", func() {
			exp := baseExperiment("suspended", ns)
			exp.Spec.Suspend = true
			Expect(k8sClient.Create(ctx, exp)).To(Succeed())

			res, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: exp.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())
			// Suspended experiments still requeue to their next fire time so
			// un-suspension is picked up promptly at the natural boundary.
			Expect(res.RequeueAfter).To(BeNumerically(">", 0))
			Expect(res.RequeueAfter).To(BeNumerically("<=", 2*time.Hour))

			got := &chaosv1alpha1.ChaosExperiment{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(exp), got)).To(Succeed())
			Expect(got.Status.Runs).To(BeZero(), "suspended runs must not be counted")
			Expect(got.Status.LastResult).To(BeEmpty(), "no result should be recorded for suspended runs")
			Expect(got.Status.NextRun).NotTo(BeNil(), "NextRun must be projected even while suspended")
		})
	})

	Context("when the schedule is not yet due", func() {
		It("updates NextRun and requeues to the fire time without running the action", func() {
			exp := baseExperiment("not-due", ns)
			Expect(k8sClient.Create(ctx, exp)).To(Succeed())

			// LastRun = now, so the next due time is one cron fire ahead.
			// Cron parser uses local time, so pin the bound loosely: at most
			// 2h ahead, strictly positive.
			exp.Status.LastRun = &metav1.Time{Time: fixedNow}
			Expect(k8sClient.Status().Update(ctx, exp)).To(Succeed())

			res, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: exp.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(BeNumerically(">", 0),
				"a not-yet-due experiment must requeue at a future fire time")
			Expect(res.RequeueAfter).To(BeNumerically("<=", 2*time.Hour),
				"cron 0 */2 must fire within 2h of any moment")

			got := &chaosv1alpha1.ChaosExperiment{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(exp), got)).To(Succeed())
			Expect(got.Status.NextRun).NotTo(BeNil())
			Expect(got.Status.NextRun.Time.After(fixedNow)).To(BeTrue())
			Expect(got.Status.Runs).To(BeZero(), "no run should be recorded when not due")
		})
	})

	Context("when the guardrail value exceeds the threshold", func() {
		It("aborts without killing pods and records reason", func() {
			// Guardrail returns 8.2, threshold is 6. Also stand up an eligible
			// pod that MUST survive - proves the abort really short-circuits.
			reconciler.NewPromAPI = newFakePromFactory(
				&model.Scalar{Value: 8.2, Timestamp: model.Now()}, nil,
			)
			pod := runningPod(ctx, ns, "must-survive")

			exp := baseExperiment("guarded", ns)
			exp.Spec.Guardrail = &chaosv1alpha1.Guardrail{
				PrometheusURL:      "http://prometheus.example:9090",
				Query:              "max(slo:service:burn_rate_1h)",
				AbortIfGreaterThan: resource.MustParse("6"),
			}
			Expect(k8sClient.Create(ctx, exp)).To(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: exp.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			got := &chaosv1alpha1.ChaosExperiment{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(exp), got)).To(Succeed())
			Expect(got.Status.LastResult).To(Equal(chaosv1alpha1.ResultAborted))
			Expect(got.Status.Runs).To(Equal(int64(1)))
			Expect(strings.Contains(got.Status.Reason, "exceeds threshold")).
				To(BeTrue(), "reason should name the threshold: %q", got.Status.Reason)

			// Pod must still exist and not be marked for deletion.
			after := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), after)).To(Succeed())
			Expect(after.DeletionTimestamp).To(BeNil(),
				"aborted run must not delete pods")
		})
	})

	Context("when the guardrail is under threshold and pods are eligible", func() {
		It("kills the requested count and records Completed", func() {
			reconciler.NewPromAPI = newFakePromFactory(
				&model.Scalar{Value: 0.5, Timestamp: model.Now()}, nil,
			)
			// Three eligible pods, kill 2.
			p1 := runningPod(ctx, ns, "victim-1")
			p2 := runningPod(ctx, ns, "victim-2")
			p3 := runningPod(ctx, ns, "victim-3")

			exp := baseExperiment("kills", ns)
			exp.Spec.Action.PodKill.Count = 2
			exp.Spec.Guardrail = &chaosv1alpha1.Guardrail{
				PrometheusURL:      "http://prometheus.example:9090",
				Query:              "max(slo:service:burn_rate_1h)",
				AbortIfGreaterThan: resource.MustParse("6"),
			}
			Expect(k8sClient.Create(ctx, exp)).To(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: exp.Name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			got := &chaosv1alpha1.ChaosExperiment{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(exp), got)).To(Succeed())
			Expect(got.Status.LastResult).To(Equal(chaosv1alpha1.ResultCompleted))
			Expect(got.Status.Runs).To(Equal(int64(1)))
			Expect(got.Status.Reason).To(ContainSubstring("killed 2"))

			// Count how many of the three originals now carry a DeletionTimestamp.
			// envtest doesn't run kubelet, so pods stay around post-delete with
			// deletionTimestamp set rather than actually going away.
			deleted := 0
			for _, p := range []*corev1.Pod{p1, p2, p3} {
				after := &corev1.Pod{}
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(p), after)
				if err != nil || after.DeletionTimestamp != nil {
					deleted++
				}
			}
			Expect(deleted).To(Equal(2), "exactly 2 of 3 pods must be marked deleted")
		})
	})
})

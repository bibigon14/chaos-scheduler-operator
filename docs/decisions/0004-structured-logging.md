# 4. Structured logging alongside operator metrics

Date: 2026-09-20
Status: Accepted

## Context

The operator ships three Prometheus metrics (`chaos_experiment_runs_total`,
`chaos_experiment_last_run_timestamp_seconds`,
`chaos_experiment_guardrail_check_duration_seconds`) covering the RED signals
for chaos experiments. In principle this satisfies "what / how much / how
fast" - the classic argument for metrics-first observability.

In practice, a 10-hour period between crons produced zero log output from the
manager pod. When a screenshot session at 08:00 PDT captured a successful pod
kill but an empty log terminal, it looked like a logger bug. Investigation
walked through zap setup, controller-runtime context propagation, watch
registration, and reconcile counters before landing on the actual cause:
every `logger.Info` call sat behind the early-return in the "not yet due"
branch of `Reconcile`, so between-cron invocations were silent by design.

Metrics answered "did it fire" (`runs_total = 8`) but not "is the controller
alive right now" or "why did it requeue instead of firing". That gap cost 40
minutes of debugging during a portfolio finalization session.

## Decision

Add structured logging at every decision branch of `Reconcile`:

- **INFO** on state changes visible in production: experiment due, guardrail
  passed / aborted / errored, podkill executing / completed / failed.
- **V(1) DEBUG** on high-frequency no-op paths: not-yet-due requeue with
  time-to-fire in structured fields (`now`, `dueAt`, `in`).

Continue exposing all three metrics unchanged - logs and metrics are
complementary, not substitutes.

## Consequences

**Positive**

- On-call has a live signal even between fires: `kubectl logs -f` shows
  requeue decisions in dev mode.
- Post-mortem of the "silent 10 hours" incident is trivially explainable via
  this ADR plus the code.
- Structured fields (`now`, `dueAt`, `in`) support `jq` filtering in
  production log aggregators.

**Negative**

- More log volume - production runs at INFO only, but V(1) is chatty when
  enabled.
- Discipline required: every future decision branch needs the same treatment
  or the gap comes back.

## Alternatives considered

- **Metrics-only.** Rejected. Counters don't explain "why silent", they only
  prove "not silent".
- **Distributed tracing / OpenTelemetry.** Overkill for a single-reconciler
  operator; adds a collector dependency to the homelab stack.
- **Register metrics eagerly with zero-value labels.** Fixes Grafana
  cold-start "no data" but doesn't solve the runtime-visibility gap;
  possible future work in a separate ADR.

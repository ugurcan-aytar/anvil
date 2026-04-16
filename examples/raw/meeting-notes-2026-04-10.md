# Infra Weekly — 2026-04-10

Attendees: Ayşe (tech lead), Bora (SRE), Cem (platform eng).

## Agenda

1. Post-mortem from the 2026-04-03 incident.
2. Resilience work still outstanding.
3. Observability investment.

## Post-mortem review

The 2026-04-03 outage was a textbook retry storm. The payments
service started returning 503s for ~90 seconds due to a db failover;
because no upstream service had a circuit breaker, retries piled up
and the outage stretched to 22 minutes. Bora wrote up the
post-mortem; we're treating it as blameless and the action items are
structural.

Decisions:

- Every outbound HTTP client gets a circuit breaker by end of Q2.
  Cem owns the library rollout; default config is 5 consecutive
  failures to open, 30s cooldown to half-open.
- The existing retry middleware stays, but the max attempts drops
  from 3 to 2 and gains exponential backoff. Ayşe owns the config
  push.
- Post-mortem culture: we'll publish every post-mortem to the internal
  wiki within 48 hours of the incident. No private retro docs.

## Observability investment

Dashboards for circuit-breaker state, retry counts, and error budget
burn rate go in this quarter. Bora will have a prototype by Friday;
Ayşe picks the Grafana org layout.

## Action items

- [ ] Cem: resilient-client library with default circuit breaker (Q2)
- [ ] Ayşe: retry config push + doc update (by 2026-04-17)
- [ ] Bora: observability dashboards prototype (by 2026-04-12)
- [ ] All: publish post-mortem within 48h going forward

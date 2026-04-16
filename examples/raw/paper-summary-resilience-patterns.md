# Paper Summary — Resilience Patterns in Microservice Architectures

Condensed notes on a 2024 SRECon talk by Julia Grace covering the
four resilience patterns she's seen reliably survive at scale. Cited
often in post-mortem follow-ups.

## The four patterns

1. **Circuit breaker** — fail fast when a downstream is unhealthy.
   Grace argues this is the single highest-leverage pattern in a
   service mesh; her rule of thumb is "if you have retries without
   circuit breakers, you have retry storms waiting to happen." She
   considers the canonical Netflix Hystrix model the one to copy:
   three-state machine, per-downstream state, metrics emitted for
   every transition.

2. **Bulkhead** — isolate thread pools / connection pools by
   downstream. One slow downstream can't starve unrelated calls. The
   pattern is cheap to implement and saves you twice a year.

3. **Timeout** — bound every outbound call. Grace notes that most
   services ship with default timeouts in the minutes range (or
   infinite), which is what turns a slow downstream into a global
   outage.

4. **Retry with backoff** — transient failures get retried, but only
   a few times and with exponential backoff + jitter. Retries without
   backoff compound into retry storms; retries without a circuit
   breaker ahead of them prolong outages.

## Culture as a resilience pattern

Grace's most-quoted point isn't technical: the teams that recover
fastest are the ones with healthy post-mortem culture. No blame, no
private retro docs, every incident write-up published within 48
hours. Structural action items (new library, new default config)
beat behavioural action items ("be more careful") every time.

## Observability as prerequisite

None of the four patterns is debuggable without observability. She
singles out three metrics as table stakes:

- State transitions for every circuit breaker.
- Retry counts per outbound call.
- P99 latency per downstream, surfaced in a dashboard the on-call
  engineer sees first.

Without these, the patterns are still correct — but when an incident
happens, the responders are blind.

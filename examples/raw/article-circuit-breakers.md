# Circuit Breakers in Distributed Systems

Circuit breakers protect a distributed system from cascading failure.
A downstream service stops responding — without a circuit breaker,
every upstream call queues against it, hogs thread pools, and starves
unrelated traffic. With a circuit breaker, upstream calls fail fast
after a threshold of consecutive errors, releasing resources.

## States

A circuit breaker has three states:

- **Closed** — traffic flows normally. Errors are counted.
- **Open** — the error count tripped the threshold. Every call fails
  immediately with a "circuit open" error, without touching the
  downstream service.
- **Half-open** — after a cooldown, a single probe request is allowed
  through. If it succeeds the breaker closes; if it fails the breaker
  re-opens.

## Relationship to retries

Retries and circuit breakers are complementary, not alternatives. A
retry policy handles transient failures (network blips, a single
dropped packet). A circuit breaker handles persistent failures (the
downstream service is actually down). Retries behind an open circuit
breaker fail fast without consuming downstream resources — that's the
healthy interaction.

Without this pairing, aggressive retries can turn a recoverable blip
into a full outage: every client retries, the downstream service
chokes under the load, and the outage lengthens. This pattern has a
name — a "retry storm" — and every serious distributed system post-
mortem eventually cites it.

## Observability

A production-ready circuit breaker emits three metrics:

1. **State transitions** — when did it open? when did it close?
2. **Error rate per downstream** — the input to the threshold.
3. **Probe outcomes** — which half-open probes succeeded vs. failed.

Without these, diagnosing a cascading failure in flight is guesswork.

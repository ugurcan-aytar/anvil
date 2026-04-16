# expected-wiki (reference only)

A rough shape of what anvil ingest tends to produce from the three
raw sources in `../raw/`. Real output varies — LLMs aren't
deterministic, slug drift happens (the v0.2.6 catalog helper keeps
it bounded), and the page count shifts by a few either way depending
on how the model decomposes the material.

Expect something like:

- ~3 entities (Ayşe, Bora, Cem — the meeting attendees) plus the
  companies / tools named in the article (Netflix, Hystrix, Grafana,
  SRECon).
- ~6-8 concepts (circuit-breaker, retry-pattern, bulkhead-pattern,
  timeout-pattern, retry-storm, post-mortem-culture,
  observability-as-prerequisite, error-budget-burn-rate).
- 1-2 synthesis pages if you `anvil ask` + `anvil save` afterwards.

Cross-references you should see:
- [[circuit-breaker]] linked from [[retry-pattern]], [[bulkhead-pattern]],
  [[retry-storm]], and the meeting-notes source summary.
- [[retry-storm]] linked from [[circuit-breaker]] and from the
  meeting-notes.
- [[post-mortem-culture]] linked from the meeting notes and the
  paper summary.

Use this directory as a sanity check on the format + cross-linking
density, not as a golden output to diff against.

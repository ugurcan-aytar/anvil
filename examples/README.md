# anvil examples

Three small sources you can run anvil against without setting up a real
corpus. They're short, overlap deliberately on a few shared concepts
(circuit breakers, retries, observability, post-mortems), and cover the
mix of genres anvil is designed to compile: a technical article, a
meeting-notes fragment, and a research-paper summary.

## Try it

    anvil init my-test
    cp -r examples/raw/* my-test/raw/
    cd my-test
    anvil ingest raw/
    anvil status
    anvil ask "How do circuit breakers and retries interact?"
    anvil lint

Expected after ingest: ~10-15 wiki pages, several cross-references
between pages like `[[circuit-breaker]]`, `[[retry-pattern]]`,
`[[post-mortem-culture]]`.

The `expected-wiki/` directory contains a reference sample so you can
see the shape of the output. LLM output isn't deterministic, so your
actual pages won't match byte-for-byte — use it for the format + rough
coverage, not a diff target.

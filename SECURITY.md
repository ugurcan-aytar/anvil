# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in anvil:

1. **Do NOT open a public GitHub issue**
2. Email: ugurcan.aytar@gmail.com
3. Include: description, steps to reproduce, potential impact

## Response Timeline

- Acknowledgment: within 48 hours
- Assessment: within 1 week
- Fix: depends on severity, typically within 2 weeks

## Scope

- API key leakage (keys in wiki pages, logs, error messages)
- Path traversal (accessing files outside project directory)
- Command injection (via source filenames, wiki page names)
- LLM prompt injection through source content

# Project Notes for Contributors and Agents

## Project context

Webhook Test Engine tests whether applications handle webhook events correctly,
including signatures, duplicate delivery, and application state.

The repository is in early development. The previous unrelated prototype has
been removed. Do not describe planned functionality as implemented.

Planned direction: a Go CLI, configurable test scenarios, provider adapters
starting with Paystack, HTTP delivery, read-only state probes, and a SQLite
example app. The scenario configuration format is not yet decided.

## Working agreement

- Inspect the repository and relevant files before editing.
- For non-trivial changes, present a short plan and wait for explicit approval.
- Keep changes small and reviewable. Preserve unrelated work and Git history.
- Follow existing conventions and prefer the standard library over new
  dependencies.
- Do not add dependencies, commit, push, rename the repository, rewrite history,
  or add a license without explicit approval.
- Never inspect, print, commit, or modify `.env` or any secret.
- For bugs, establish expected versus actual behavior, find the root cause, and
  add a regression test where practical.
- Report the exact validation commands run and their results. Never claim a
  check passed unless it was actually run.

## Learning-first workflow

For unfamiliar core logic, discuss the design before implementation and let the
user make important decisions. Explain trade-offs and verification methods.
After a few genuine attempts, provide a direct explanation rather than prolonging
questions. End meaningful tasks with what changed, what was verified, known
limitations, and one concept worth retaining.

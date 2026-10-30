# Webhook Test Engine

Webhook Test Engine checks whether an application handles webhook events
correctly, not just whether its HTTP endpoint returns a successful status code.

A webhook handler can return `200 OK` and still be wrong. It may accept an
invalid signature, fail to persist the expected state, or store the wrong
payment details. This project delivers a realistic webhook and then inspects
the application's persistent state to verify the business outcome.

## Implemented milestone

The repository currently implements one end-to-end Paystack `charge.success`
scenario:

- The engine builds a synthetic payment event.
- It signs the exact JSON request body with HMAC-SHA512.
- It sends one HTTP POST with the `X-Paystack-Signature` header.
- The example application verifies the signature and writes the payment to
  SQLite.
- The engine opens the database read-only and polls for the expected payment.
- The test passes only when the HTTP response is 2xx **and** the stored
  reference, amount, currency, and status all match.

An HTTP 200 response without the expected database state is reported as a
failure. This is the defining behaviour of the project. A non-2xx response also
fails the scenario, even when the stored payment happens to match.

This is the only implemented scenario. Duplicate delivery, retries, other
providers, and scenario files are not implemented.

## Requirements

- Go 1.23.4 or newer.
- No CGO toolchain: SQLite is provided by `modernc.org/sqlite`.

## Repository layout

```text
cmd/
  example-app/     SQLite-backed receiver used as the application under test
  webhook-test/    test runner and CLI
internal/
  engine/          delivery, polling, and PASS/FAIL evaluation
  exampleapp/      webhook handler and SQLite persistence
  paystack/        Paystack event payload and HMAC-SHA512 signatures
  provider/        small adapter boundary between engine and provider
```

## Run the example

Start the example application in one terminal:

```powershell
go run ./cmd/example-app
```

The example app defaults to:

```text
--addr 127.0.0.1:8080
--db example.db
--secret dev-secret
```

`dev-secret` is a clearly labelled development-only secret. Do not use a real
Paystack key with this example.

In a second terminal, run the test:

```powershell
go run ./cmd/webhook-test `
  --target http://127.0.0.1:8080/webhooks/paystack `
  --db .\example.db `
  --secret dev-secret
```

A successful run prints:

```text
PASS paystack reference=whe_... http=200 state=verified
```

A failure prints a `FAIL` line with the HTTP status and the reason, for example:

```text
FAIL paystack reference=whe_... http=200: expected payment "whe_..." not found before timeout
```

The engine exits with:

- `0` when the webhook was delivered and the expected state was verified.
- `1` when delivery, HTTP, database, timeout, or state verification failed.
- `2` for invalid command-line usage.

These are the exit codes of the compiled `webhook-test` binary. `go run`
reports its own status and can collapse a `2` into `1`, so build the binary
when a script depends on the exact code.

### Engine flags

| Flag | Default | Meaning |
|---|---|---|
| `--target` | required | Absolute HTTP or HTTPS webhook endpoint |
| `--db` | required | Path to the application SQLite database |
| `--secret` | required | Webhook signing secret; use a development-only value |
| `--reference` | generated | Payment reference to send and verify |
| `--amount` | `50000` | Amount in minor currency units |
| `--currency` | `NGN` | Payment currency |
| `--timeout` | `2s` | HTTP delivery and state verification timeout |

Amounts use integer minor currency units. For NGN, `50000` means 500.00 NGN
(kobo). The engine does not convert or round amounts.

## Database contract

Version 1 requires SQLite. The engine opens the database read-only and expects
a table named `payments` with at least these columns:

```sql
CREATE TABLE payments (
    reference TEXT PRIMARY KEY,
    amount    INTEGER NOT NULL,
    currency  TEXT NOT NULL,
    status    TEXT NOT NULL
);
```

`reference` must uniquely identify the payment, `amount` must be stored in
integer minor currency units, `currency` must be the currency code sent in the
event, and `status` must be the value expected by the test (`success` for
`charge.success`). The example application also stores a `received_at` column;
the engine does not query it.

The database file must already exist and contain this table before the test
runs. The engine does not create or migrate the schema. Arbitrary application
schemas, configurable table or column names, and other database engines are not
supported yet. To test a different application in v1, provide a compatible
SQLite database and `payments` table.

## How it works

1. The Paystack adapter builds a minimal `charge.success` event and signs the
   exact serialized body with HMAC-SHA512.
2. The engine checks that the reference does not already exist in SQLite, so an
   old row cannot produce a false pass.
3. The engine sends one HTTP POST with a bounded timeout.
4. Non-2xx responses fail immediately.
5. The engine opens SQLite read-only and polls every 50 ms until the configured
   timeout.
6. The expected reference, amount, currency, and status must all match.

Database errors are reported distinctly from a missing payment row. A failed
query is never treated as evidence that no payment exists.

## Tests

Run the automated tests with:

```powershell
go test ./...
```

The suite runs without network access or real provider credentials: every test
uses a temporary SQLite database and a local `httptest` server.

Coverage includes:

- HMAC-SHA512 signatures checked against a fixed, independently computed
  vector, plus changed-body and wrong-secret rejection.
- Correctly signed events persisted, and invalid signatures, malformed JSON,
  and unsupported events rejected without writing a row.
- An end-to-end pass where the expected reference, amount, currency, and status
  are all present in SQLite.
- A failure when a handler returns HTTP 200 without writing any state, and when
  it persists the wrong amount.
- A failure on a non-2xx response even when the persisted state happens to
  match.
- Refusal to send when the reference already exists, so an old row cannot
  produce a false pass.
- Distinct failures for missing databases, missing schema, and delivery
  timeouts.

## Status and direction

Implemented:

- Go CLI and engine orchestration
- Paystack `charge.success` event generation and HMAC-SHA512 signatures
- One HTTP webhook delivery with non-2xx rejection
- Read-only SQLite state verification
- SQLite-backed example application
- PASS/FAIL reporting and CI-friendly exit codes
- Automated tests, including the HTTP-200-without-state and non-2xx regressions

Planned, but not implemented:

- Duplicate-delivery and retry scenarios
- Additional Paystack event types
- Other payment providers and webhook systems
- A scenario configuration format, which is still undecided
- Additional state probes and example applications

The broader goal is a general webhook testing engine built around small
provider adapters, not a Paystack-only tool. The core engine currently depends
on a provider adapter interface; all Paystack payload and signature details live
in `internal/paystack`.

## Out of scope for this milestone

- Live or test Paystack API calls
- Real money movement
- Load or performance testing
- Docker and CI configuration
- A dashboard or web interface

## License

Not yet chosen.

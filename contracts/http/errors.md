# Error codes

Every error is an [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) problem
(`application/problem+json`) with a stable `code` and the `request_id` to
quote in support requests. `detail` is safe to show; internal errors never
leak database or stack details.

| HTTP | code | Meaning |
|---|---|---|
| 400 | `malformed_body` | Not exactly one JSON object with only the documented fields (unknown fields such as `status` are rejected) |
| 400 | `idempotency_key_required` | `POST /v1/claims` without `Idempotency-Key` |
| 400 | `idempotency_key_invalid` | Key is not 8-128 characters of `A-Z a-z 0-9 . _ : -` |
| 400 | `invalid_parameter` | Query parameter out of range (e.g. `limit` > 100) |
| 401 | `unauthenticated` | Missing, malformed, expired or untrusted access token |
| 401 | `invalid_signature` | Internal ingestion only: missing or invalid HMAC signature |
| 403 | `forbidden` | Valid token, but the caller's group may not use this route |
| 403 | `self_decision_forbidden` | A reviewer tried to decide their own claim |
| 404 | `not_found` | No such claim, **or the caller may not see it** (existence is not revealed) |
| 409 | `already_decided` | The claim was decided before (by someone else, or differently) |
| 409 | `event_conflict` | Internal ingestion: the event ID was recorded with different content |
| 413 | `body_too_large` | Body over the limit (4 KiB for the public API, 64 KiB for events) |
| 415 | `unsupported_media_type` | Body is not `application/json` |
| 422 | `validation_failed` | Field values break a rule (amount, description) |
| 422 | `idempotency_key_reused` | Same `Idempotency-Key` with a different request body |
| 422 | `invalid_event`, `unsupported_schema_version` | Internal ingestion: event rejected permanently |
| 500 | `internal` | Unexpected error; logged with the request ID |
| 503 | `not_ready`, `unavailable` | A dependency (database) is unavailable; retry later |

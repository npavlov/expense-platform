# Event contracts

## ExpenseDecided

| | |
|---|---|
| Producer | `expenses`, written to `expenses.outbox` in the decision transaction |
| Consumer | `audit`, `POST /internal/v1/events` |
| Schema | [`expense-decided.v1.schema.json`](expense-decided.v1.schema.json) |
| Delivery | At least once, over HTTP, signed with HMAC-SHA256 (see below) |
| Ordering | Not guaranteed and not needed: one decision event per claim |

### Identity and deduplication

`event_id` (UUIDv7) is generated once, in the same transaction as the decision,
and stored with the payload. Every redelivery sends the identical bytes.
The consumer records each `event_id` at most once:

| Consumer sees | Response | Producer does |
|---|---|---|
| New `event_id` | `201 recorded` | marks delivered |
| Known `event_id`, same content | `200 duplicate` | marks delivered |
| Known `event_id`, different content | `409 event_conflict` | parks the event (producer bug) |
| Unknown `event_type` or `schema_version` | `422 unsupported_schema_version` | parks the event |
| Invalid content | `422 invalid_event` | parks the event |
| Bad or missing signature | `401 invalid_signature` | parks the event (configuration error) |
| Database unavailable | `503` | retries with backoff |
| Timeout, connection error, 5xx, 408, 429 | — | retries with backoff |

"Same content" is compared on the fields the consumer understands, so a
retry that differs only in JSON formatting is still a duplicate.

### Compatibility rules

- **Additive changes** (a new optional field) keep `schema_version: 1`.
  Consumers ignore unknown fields;
  [`examples/expense-decided.v1-additive.json`](examples/expense-decided.v1-additive.json)
  is accepted by today's consumer.
- **Breaking changes** (removing, renaming or retyping a field, changing
  semantics) increment `schema_version`. The consumer is upgraded first to
  accept both versions; only then does the producer switch.
  [`examples/expense-decided.v2.json`](examples/expense-decided.v2.json)
  shows that today's consumer rejects an unknown major version instead of
  misreading it.
- Both services test against these files: `expenses` validates the events it
  produces against the schema, `audit` parses every example.

### Signature

```
X-Expense-Signature: t=<unix seconds>,kid=<key id>,v1=<hex HMAC-SHA256(key, t + "." + raw body)>
```

The receiver accepts timestamps within ±5 minutes and any key listed in
`INGEST_SIGNING_KEYS`, so keys rotate without downtime (add new key on the
receiver, switch the sender, remove the old key). See
`libs/platform/sigauth`.

`X-Request-ID` carries the original API request ID, so the receiver's logs
use the same request ID as the producer's.

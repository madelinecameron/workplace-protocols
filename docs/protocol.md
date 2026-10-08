# Correlation

The `protocol` package links records across systems that are owned
separately. It uses the attribute names from the
[CloudEvents correlation extension](https://github.com/cloudevents/spec/blob/main/cloudevents/extensions/correlation.md),
plus one extension attribute of its own.

| Attribute | Source | Meaning |
|---|---|---|
| `correlationid` | CloudEvents correlation extension | The fixed root of a whole workflow. Required. |
| `causationid` | CloudEvents correlation extension | The immediate input that produced this record. |
| `sessionid` | This package | The agent run that handled this hop. |

CloudEvents accepts any non-empty string for these. This package is stricter:
every value must be a namespaced reference, `namespace:value`, such as
`mail:thread/42`. The namespace says which system minted the id, so ids from
different systems cannot collide. `ValidateRef` checks the format and
`schema/v2/correlation.schema.json` describes it as JSON Schema.

## Using it with CloudEvents

`Correlation.Attributes` returns the attributes to set on an event, and
`FromAttributes` reads them back:

```go
c := protocol.Correlation{CorrelationID: "mail:thread/42", CausationID: "mail:message/7"}
for name, value := range c.Attributes() {
	event.SetExtension(name, value)
}
```

A system that does not use CloudEvents can embed `Correlation` as a JSON
object; it marshals to the same attribute names.

## Mapping

| Record | `correlationid` | `causationid` |
|---|---|---|
| Message in a threaded mailbox | `mail:thread/<thread_id>` | `mail:message/<in_reply_to>` when replying |
| Model call made while handling a message | the message's thread | the message |
| Scheduled job | `<job>:run/<run_id>` | the step that triggered the call |

For tracing that crosses into OpenTelemetry, also set the CloudEvents
[distributed tracing extension](https://github.com/cloudevents/spec/blob/main/cloudevents/extensions/distributed-tracing.md)
(`traceparent`). Correlation is the business workflow; `traceparent` is the
span tree. They can coexist on one event.

## What it is not

Correlation is not identity or authorization. Choosing a `correlationid`
grants no authority, and a receiver must authenticate and authorize a request
exactly as it would without one.

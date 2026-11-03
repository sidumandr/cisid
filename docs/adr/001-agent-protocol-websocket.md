# ADR 001: The agent ↔ server protocol stays on WebSocket

**English** · [Türkçe](../tr/adr/001-agent-protokolu-websocket.md)

- **Status:** Accepted
- **Date:** 2026-11-03
- **Related:** [ROADMAP.md](../../ROADMAP.md) Phase 4, [agent.md](../agent.md), [hub.md](../hub.md)

## Context

The ROADMAP had an item for Phase 4: "Move agent ↔ server communication to gRPC (`api/proto`)". That is why the project skeleton set aside the `api/proto` directory.

Today the protocol is JSON envelopes over a single WebSocket connection (`{id, type, payload}`, types in `pkg/models/protocol.go`). The protocol does its real work in the application layer, not in the transport layer:

- **Acknowledged delivery:** The agent keeps logs and results in an outbox. The server sends batched `ack`s. If the connection drops, unacknowledged messages are resent, and the server filters out duplicates.
- **Ownership:** Jobs are bound to an agent with a lease. When the agent reconnects, it takes its job back with `running_job_id` in `agent_hello` (`AdoptJob`).
- **Drain:** When the server shuts down, it sends `server_closing`. The agent flushes its pending messages and connects to another server.
- **Cancellation:** `job_cancel` goes from the server to the agent.

All of this is tested for network outages, server restarts and lost agents (connection-cutting tests through a TCP proxy, mutation checks).

## Decision

The agent ↔ server protocol stays WebSocket + JSON for now. It does not move to gRPC. The `api/proto` directory stays reserved in case it is needed later.

## Rationale

1. **Delivery guarantees do not depend on the transport.** A gRPC bidirectional stream also loses messages when the connection drops. The ack, resend, deduplication and lease logic would have to be written the same way in gRPC. The switch would not simplify this code, only move it to another transport.
2. **Network compatibility.** WebSocket is an HTTP/1.1 upgrade. It passes through corporate HTTP proxies, L7 load balancers that speak HTTP/1.1 and most reverse proxies with no extra configuration. gRPC needs HTTP/2 end to end and needs separate configuration on some proxies and load balancers.
3. **One port, one certificate.** The agent endpoint (`/agent/ws`) runs on the same `net/http` server as the webhook, the API and the web UI, on the same port and with the same TLS certificate. gRPC needs either a separate port or an extra layer that splits HTTP/1.1 and HTTP/2 on the same port.
4. **Dependencies and build.** gRPC brings in `google.golang.org/grpc`, protobuf and code generation (`protoc` or `buf`). Today the build is just `go build`.
5. **Messages are small and infrequent.** Most of the traffic is log chunks and acks. There is no sign that JSON serialization is a bottleneck at the current scale. This was not measured: it is only an assumption.

## Consequences

- The protocol contract is the Go types (`pkg/models/protocol.go`) and the [agent.md](../agent.md#protocol) document. There is no schema file and no generated client.
- Anyone who wants to write an agent in a language other than Go has to implement the protocol from the document.
- Changes to the message types must stay backward compatible: new fields are added as optional, and unknown fields are ignored.

## When to revisit

- If agents outside Go are needed and generating clients from a schema becomes worthwhile.
- If measurements show that JSON serialization or WebSocket framing is a bottleneck.
- If large, streaming data transfers such as artifact uploads are added. In that case a separate HTTP endpoint is also an option.

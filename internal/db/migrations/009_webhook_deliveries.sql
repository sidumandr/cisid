-- Webhook payloads already turned into jobs, keyed by the SHA-256 of the
-- signed body: a replayed or redelivered payload is recognised even when
-- its (unsigned) delivery header differs.
CREATE TABLE webhook_deliveries (
    payload_sha256 TEXT        PRIMARY KEY,
    delivery_id    TEXT,
    received_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX webhook_deliveries_received_idx ON webhook_deliveries (received_at);

-- W3C traceparent of the span that queued the job (webhook or rerun), so
-- dispatching and running the job continue the same trace. Empty when
-- tracing is off.
ALTER TABLE jobs ADD COLUMN trace_parent TEXT NOT NULL DEFAULT '';

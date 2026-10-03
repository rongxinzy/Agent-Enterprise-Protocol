-- The model catalog grows the anthropic wire protocol: gateway models whose
-- protocol is anthropic render as a self-contained EnvoyFilter passthrough
-- (per-model path prefix) instead of an ai-proxy WasmPlugin route. Widen the
-- protocol CHECK; every existing row stays openai-compatible.
ALTER TABLE models DROP CONSTRAINT models_protocol_check;
ALTER TABLE models ADD CONSTRAINT models_protocol_check
  CHECK (protocol IN ('openai-compatible', 'anthropic'));

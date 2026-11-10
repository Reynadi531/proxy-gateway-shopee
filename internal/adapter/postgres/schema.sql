CREATE TABLE IF NOT EXISTS gateway_transactions (
 transaction_id text PRIMARY KEY,
 scope text NOT NULL,
 amount bigint NOT NULL CHECK (amount > 0),
 unique_code bigint NOT NULL CHECK (unique_code BETWEEN 1 AND 999),
 total_amount bigint NOT NULL CHECK (total_amount = amount + unique_code),
 qris text NOT NULL,
 webhook_url text NOT NULL,
 status text NOT NULL CHECK (status IN ('pending','paid','expired')),
 provider_transaction_id text,
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS gateway_pending_amount ON gateway_transactions(scope,total_amount) WHERE status='pending';
CREATE UNIQUE INDEX IF NOT EXISTS gateway_provider_match ON gateway_transactions(scope,provider_transaction_id) WHERE provider_transaction_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS gateway_merchant_transactions (
 scope text NOT NULL,
 provider_transaction_id text NOT NULL,
 amount bigint NOT NULL,
 status text NOT NULL,
 occurred_at timestamptz NOT NULL,
 raw jsonb NOT NULL,
 PRIMARY KEY(scope,provider_transaction_id)
);
CREATE TABLE IF NOT EXISTS gateway_scan (
 scope text PRIMARY KEY,
 scanned_until timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS gateway_webhooks (
 event_id text PRIMARY KEY,
 transaction_id text NOT NULL UNIQUE REFERENCES gateway_transactions(transaction_id),
 url text NOT NULL,
 payload jsonb NOT NULL,
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 delivered_at timestamptz,
 last_error text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS gateway_webhooks_due ON gateway_webhooks(next_attempt_at) WHERE delivered_at IS NULL;

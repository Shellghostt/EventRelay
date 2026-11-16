-- 0003_m3_retry_policy.sql
-- Normalize M2 dead-letter reasons to the ADR 0005 taxonomy, then enforce it.
-- M2 wrote the raw attempt outcome as the reason; exhausted attempts written
-- through fenced completion carried 'retryable', 'timeout' or 'network', and
-- non-retryable responses carried 'non_retryable'. Lease-expiry recovery
-- already wrote 'attempts_exhausted'. No other values exist, so the CHECK
-- below fails closed if any row is unexpected.

UPDATE dead_letters SET reason = 'attempts_exhausted'
WHERE reason IN ('retryable', 'timeout', 'network');

UPDATE dead_letters SET reason = 'non_retryable_response'
WHERE reason = 'non_retryable';

ALTER TABLE dead_letters ADD CONSTRAINT dead_letters_reason_check
    CHECK (reason IN ('attempts_exhausted', 'non_retryable_response', 'non_retryable_policy'));

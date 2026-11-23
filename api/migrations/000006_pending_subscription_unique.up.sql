-- Clean up any pre-existing duplicate pending subscriptions per (user_id, plan_id),
-- keeping the earliest row by created_at (payments cascade via ON DELETE CASCADE).
WITH ranked AS (
  SELECT id,
         ROW_NUMBER() OVER (PARTITION BY user_id, plan_id ORDER BY created_at ASC, id ASC) AS rn
  FROM subscriptions
  WHERE status = 'pending'
)
DELETE FROM subscriptions s
USING ranked r
WHERE s.id = r.id AND r.rn > 1;

CREATE UNIQUE INDEX IF NOT EXISTS subscriptions_user_plan_pending_idx
  ON subscriptions (user_id, plan_id)
  WHERE status = 'pending';

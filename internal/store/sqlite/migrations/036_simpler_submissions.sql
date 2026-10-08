-- 036: a submission keeps what the visitor typed and where the visit came
-- from (referrer and UTMs), nothing else. The page, the visitor and the
-- environment are its $form_submit event's, which has the same id.
--
-- The visit snapshot's referrer and UTMs move to their own columns and onto
-- the submission's event while it is still in the raw window; its landing
-- path and view count are dropped, as are host, path, via and the actor.
ALTER TABLE submissions ADD COLUMN referrer     TEXT NOT NULL DEFAULT '';
ALTER TABLE submissions ADD COLUMN utm_source   TEXT NOT NULL DEFAULT '';
ALTER TABLE submissions ADD COLUMN utm_medium   TEXT NOT NULL DEFAULT '';
ALTER TABLE submissions ADD COLUMN utm_campaign TEXT NOT NULL DEFAULT '';

UPDATE submissions SET
  referrer     = COALESCE(json_extract(visit, '$.referrer'), ''),
  utm_source   = COALESCE(json_extract(visit, '$.utm_source'), ''),
  utm_medium   = COALESCE(json_extract(visit, '$.utm_medium'), ''),
  utm_campaign = COALESCE(json_extract(visit, '$.utm_campaign'), '')
WHERE visit IS NOT NULL;

UPDATE events SET
  referrer_source = s.referrer,
  utm_source      = s.utm_source,
  utm_medium      = s.utm_medium,
  utm_campaign    = s.utm_campaign
FROM submissions s
WHERE events.family = 'product' AND events.event_name = '$form_submit'
  AND events.project_id = s.project_id AND events.id = s.id
  AND s.visit IS NOT NULL;

ALTER TABLE submissions DROP COLUMN visit;
ALTER TABLE submissions DROP COLUMN host;
ALTER TABLE submissions DROP COLUMN path;
ALTER TABLE submissions DROP COLUMN via;
ALTER TABLE submissions DROP COLUMN actor_kind;
ALTER TABLE submissions DROP COLUMN actor_id;

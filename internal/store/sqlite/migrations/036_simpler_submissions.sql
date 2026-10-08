-- 036: a submission keeps what the visitor typed and where the visit came
-- from, nothing else. The page, the visitor and the environment are its
-- $form_submit event's, which has the same id.
--
-- visit becomes attribution: a JSON object of the visit's referrer and UTMs
-- (referrer, utm_source, utm_medium, utm_campaign), empty values left out,
-- NULL when no visit matched; a new key needs no migration. Its landing
-- path and view count are dropped, as are host, path, via and the actor.
-- The referrer and UTMs are copied onto the submission's event too, while
-- it is still in the raw window.
UPDATE events SET
  referrer_source = COALESCE(json_extract(s.visit, '$.referrer'), ''),
  utm_source      = COALESCE(json_extract(s.visit, '$.utm_source'), ''),
  utm_medium      = COALESCE(json_extract(s.visit, '$.utm_medium'), ''),
  utm_campaign    = COALESCE(json_extract(s.visit, '$.utm_campaign'), '')
FROM submissions s
WHERE events.family = 'product' AND events.event_name = '$form_submit'
  AND events.project_id = s.project_id AND events.id = s.id
  AND s.visit IS NOT NULL;

ALTER TABLE submissions RENAME COLUMN visit TO attribution;
UPDATE submissions SET attribution = (
  SELECT CASE WHEN COUNT(*) = 0 THEN NULL ELSE json_group_object(key, value) END
  FROM json_each(submissions.attribution)
  WHERE key IN ('referrer', 'utm_source', 'utm_medium', 'utm_campaign') AND value <> '')
WHERE attribution IS NOT NULL;

ALTER TABLE submissions DROP COLUMN host;
ALTER TABLE submissions DROP COLUMN path;
ALTER TABLE submissions DROP COLUMN via;
ALTER TABLE submissions DROP COLUMN actor_kind;
ALTER TABLE submissions DROP COLUMN actor_id;

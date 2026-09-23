-- Empty-database guard: see the note in v_views_daily.sql. A brand-new install
-- has no projects row until the first config sync, and a zero-row result here
-- fails the source build. index.md filters the sentinel out on `id != 0`,
-- an id AUTOINCREMENT never issues.
--
-- archived is 0/1 and not the archived_at timestamp, which is load-bearing.
-- The sqlite connector infers a column's type from its first non-null value,
-- then converts every value in it with `new Date(value)` -- nulls included,
-- and `new Date(null)` is 1970-01-01, not null. Shipping archived_at makes
-- `archived_at is null` false for every project and files them all under
-- Archived. Deciding it in SQLite, where the null is still a null, is the fix;
-- see the type-inference note in identities.sql for the sibling trap.
select id, name,
       case when archived_at is null then 0 else 1 end as archived
from projects
union all
select 0, '', 0 where not exists (select 1 from projects)

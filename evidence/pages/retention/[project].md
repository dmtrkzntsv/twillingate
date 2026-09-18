# {params.project} — Retention

```sql retention_mode
select identity from twillingate.projects where alias = '${params.project}'
```

{#if retention_mode[0].identity === 'identified'}

Each actor is cohorted on one surface, fixed on its first day: **app** if it
sent a screen view, else **web** if it sent a pageview, else **product** (seen
through custom events alone). A browser visitor id and an app `install_id` are
different actors even for the same person, so blending the curves would
describe no population.

```sql retention_curve
-- A cohort with nobody back on day k has no row at offset k, so pooling
-- only the rows that exist would drop it from the denominator and read
-- high. Build the full cohort x offset grid instead, keeping the offsets a
-- cohort has actually reached. The latest aggregated day is left out: the
-- 03:00 pass computes it from the first three hours only.
-- user_retention is null, not 0, where no cohort holds a signed-in user.
with rows as (
  select surface, cohort_day::date as cohort_day, day_offset::int as day_offset,
         actors, cohort_size,
         users, user_cohort_size
  from twillingate.v_retention
  where project = '${params.project}'
),
through as (select max(cohort_day + day_offset) as last_day from rows),
cohorts as (
  select surface, cohort_day, cohort_size, user_cohort_size from rows where day_offset = 0
),
grid as (
  select c.*, o.day_offset
  from cohorts c
  cross join (select unnest(range(0, 31))::int as day_offset) o
  where c.cohort_day + o.day_offset < (select last_day from through)
),
filled as (
  select g.surface, g.day_offset, g.cohort_size, g.user_cohort_size,
         coalesce(r.actors, 0) as actors, coalesce(r.users, 0) as users
  from grid g
  left join rows r
    on r.surface = g.surface and r.cohort_day = g.cohort_day and r.day_offset = g.day_offset
)
select surface, day_offset,
       sum(actors) as actors, sum(cohort_size) as cohort_size,
       case when sum(cohort_size) > 0
            then sum(actors) * 1.0 / sum(cohort_size) else 0 end as retention,
       sum(users) as users, sum(user_cohort_size) as user_cohort_size,
       case when sum(user_cohort_size) > 0
            then sum(users) * 1.0 / sum(user_cohort_size) end as user_retention
from filled
group by surface, day_offset
order by surface, day_offset
```

```sql user_curve
select * from ${retention_curve} where user_retention is not null
```

```sql retention_milestones
select surface,
       max(case when day_offset = 1 then retention end) as d1,
       max(case when day_offset = 7 then retention end) as d7,
       max(case when day_offset = 30 then retention end) as d30,
       max(case when day_offset = 1 then user_retention end) as user_d1,
       max(case when day_offset = 7 then user_retention end) as user_d7,
       max(case when day_offset = 30 then user_retention end) as user_d30,
       max(case when day_offset = 0 then user_cohort_size end) as users_cohorted
from ${retention_curve}
where day_offset in (0, 1, 7, 30)
group by surface
order by surface
```

```sql user_milestones
select * from ${retention_milestones} where users_cohorted > 0
```

```sql retention_cohorts
select surface, cohort_day, day_offset, cohort_size, actors,
       case when cohort_size > 0 then actors * 1.0 / cohort_size else 0 end as retention,
       user_cohort_size, users,
       case when user_cohort_size > 0 then users * 1.0 / user_cohort_size end as user_retention
from twillingate.v_retention
where project = '${params.project}' and day_offset between 0 and 30
order by cohort_day desc, surface, day_offset
```

{#if user_curve.length > 0}

## Signed-in users

Actors that sent a `user_id` — the people you can recognise when they come
back. Visitors who never signed in are left out here; they are under
Everyone below.

<DataTable data={user_milestones} rows=5 title="Milestones">
    <Column id=surface title="Surface" />
    <Column id=user_d1 title="D1" fmt=pct1 />
    <Column id=user_d7 title="D7" fmt=pct1 />
    <Column id=user_d30 title="D30" fmt=pct1 />
</DataTable>

<LineChart data={user_curve} x=day_offset y=user_retention series=surface yFmt=pct1
  title="Signed-in retention by day offset" />

{/if}

## Everyone

Every actor, signed in or not. A visitor who never signs in is recognised on
return only if the client keeps a stable `$install_id`; a client that mints
one per page load makes each visit a new actor, and this curve reads near
zero.

<DataTable data={retention_milestones} rows=5 title="Milestones">
    <Column id=surface title="Surface" />
    <Column id=d1 title="D1" fmt=pct1 />
    <Column id=d7 title="D7" fmt=pct1 />
    <Column id=d30 title="D30" fmt=pct1 />
</DataTable>

<LineChart data={retention_curve} x=day_offset y=retention series=surface yFmt=pct1
  title="Retention by day offset" />

## Cohorts

Users, Users back and User rate are the signed-in subset of each cohort.

<DataTable data={retention_cohorts} rows=20 search=true>
    <Column id=cohort_day title="Cohort" />
    <Column id=surface title="Surface" />
    <Column id=day_offset title="Day" fmt=num0 />
    <Column id=cohort_size title="Size" fmt=num0 />
    <Column id=actors title="Returned" fmt=num0 />
    <Column id=retention title="Retention" fmt=pct1 contentType=colorscale />
    {#if user_curve.length > 0}
        <Column id=user_cohort_size title="Users" fmt=num0 />
        <Column id=users title="Users back" fmt=num0 />
        <Column id=user_retention title="User rate" fmt=pct1 contentType=colorscale />
    {/if}
</DataTable>

{:else}

Retention is undefined in **anonymous** identity mode: `actor_id` rotates at
midnight, so every cohort would contain only its own first day.

Run <code class="markdown">twillingate project update -alias {params.project} -identity identified</code>
(or the `update_project` MCP tool) to enable cohorts. Note that identified
mode stores a persistent `localStorage` id on the web, which is
terminal-equipment storage under ePrivacy — the same legal category as a
cookie.

{/if}

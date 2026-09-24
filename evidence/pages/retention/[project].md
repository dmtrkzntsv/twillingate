# {project_name[0].name} — Retention

```sql project_name
select name from twillingate.projects where id = '${params.project}'
```

<ReportNav project={params.project} current="retention" />

Cohorts exist for actors identified by a `$user_id` or a stable
`$install_id`. A project whose clients send neither has none: the tables
below stay empty until they do.

<ButtonGroup name=range title="Cohorts from">
    <ButtonGroupItem value="30" valueLabel="Last 30 days" />
    <ButtonGroupItem value="90" valueLabel="Last 90 days" default />
    <ButtonGroupItem value="180" valueLabel="Last 180 days" />
    <ButtonGroupItem value="365" valueLabel="Last 365 days" />
</ButtonGroup>

The window selects cohorts by first day: actors first seen inside it, followed
for their first 45 days. A young cohort contributes only the offsets it has
reached, so D45 needs cohorts at least 45 days old.

Cohorts are kept apart by how the actor was identified: **user** (the client
sent a `$user_id`) and **install** (a stable `$install_id`) describe different
populations, and blending their curves would describe neither. An actor known
only by its connection hash is not cohorted at all — that hash rotates with
the salt, so it can never appear in a later cohort.

```sql retention_curve
-- A cohort with nobody back on day k has no row at offset k, so pooling
-- only the rows that exist would drop it from the denominator and read
-- high. Build the full cohort x offset grid instead, keeping the offsets a
-- cohort has actually reached.
--
-- "Reached" is measured against the clock, not the data: the latest day with
-- any activity is not the latest day processed, and cutting there would drop
-- complete days nobody came back on. Today is partial, and so is yesterday
-- until the 03:00 UTC pass recounts it, hence the three-hour lag.
with rows as (
  select actor_kind, cohort_day::date as cohort_day, day_offset::int as day_offset,
         actors, cohort_size
  from twillingate.v_retention
  where project_id = '${params.project}'
    and cohort_day::date >= (now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day
),
cohorts as (
  select actor_kind, cohort_day, cohort_size from rows where day_offset = 0
),
grid as (
  select c.*, o.day_offset
  from cohorts c
  cross join (select unnest(range(0, 46))::int as day_offset) o
  where c.cohort_day + o.day_offset < ((now() at time zone 'UTC') - interval 3 hour)::date
),
filled as (
  select g.actor_kind, g.day_offset, g.cohort_size,
         coalesce(r.actors, 0) as actors
  from grid g
  left join rows r
    on r.actor_kind = g.actor_kind and r.cohort_day = g.cohort_day
   and r.day_offset = g.day_offset
)
select actor_kind, day_offset,
       sum(actors) as actors, sum(cohort_size) as cohort_size,
       case when sum(cohort_size) > 0
            then sum(actors) * 1.0 / sum(cohort_size) else 0 end as retention
from filled
group by actor_kind, day_offset
order by actor_kind, day_offset
```

```sql retention_milestones
select actor_kind,
       max(case when day_offset = 7 then retention end) as d7,
       max(case when day_offset = 30 then retention end) as d30,
       max(case when day_offset = 45 then retention end) as d45
from ${retention_curve}
where day_offset in (7, 30, 45)
group by actor_kind
order by actor_kind
```

```sql user_curve
select * from ${retention_curve} where actor_kind = 'user'
```

```sql user_milestones
select * from ${retention_milestones} where actor_kind = 'user'
```

```sql install_curve
select * from ${retention_curve} where actor_kind = 'install'
```

```sql install_milestones
select * from ${retention_milestones} where actor_kind = 'install'
```

```sql retention_cohorts
select actor_kind, cohort_day, day_offset, cohort_size, actors,
       case when cohort_size > 0 then actors * 1.0 / cohort_size else 0 end as retention
from twillingate.v_retention
where project_id = '${params.project}' and day_offset between 0 and 45
  and cohort_day >= strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
order by cohort_day desc, actor_kind, day_offset
```

{#if user_curve.length > 0}

## Signed-in users

Actors the client named with a `$user_id` — the people you can recognise
whenever they come back, on any device.

<DataTable data={user_milestones} rows=5 title="Milestones">
    <Column id=d7 title="D7" fmt=pct1 />
    <Column id=d30 title="D30" fmt=pct1 />
    <Column id=d45 title="D45" fmt=pct1 />
</DataTable>

<LineChart data={user_curve} x=day_offset y=retention yFmt=pct1
  title="Signed-in retention by day offset" />

{/if}

{#if install_curve.length > 0}

## Stable installs

Actors known by an `$install_id` and never signed in. The id has to survive a
page load or app restart to mean anything: a client that mints one per page
load makes each visit a new actor, and this curve reads near zero.

<DataTable data={install_milestones} rows=5 title="Milestones">
    <Column id=d7 title="D7" fmt=pct1 />
    <Column id=d30 title="D30" fmt=pct1 />
    <Column id=d45 title="D45" fmt=pct1 />
</DataTable>

<LineChart data={install_curve} x=day_offset y=retention yFmt=pct1
  title="Install retention by day offset" />

{/if}

## Cohorts

<DataTable data={retention_cohorts} rows=20 search=true>
    <Column id=cohort_day title="Cohort" />
    <Column id=actor_kind title="Identified by" />
    <Column id=day_offset title="Day" fmt=num0 />
    <Column id=cohort_size title="Size" fmt=num0 />
    <Column id=actors title="Returned" fmt=num0 />
    <Column id=retention title="Retention" fmt=pct1 contentType=colorscale />
</DataTable>

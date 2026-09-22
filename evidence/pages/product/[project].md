# {project_name[0].name} — Product

```sql project_name
select name from twillingate.projects where id = '${params.project}'
```

<ButtonGroup name=range title="Date range">
    <ButtonGroupItem value="1" valueLabel="Last 1 day" />
    <ButtonGroupItem value="7" valueLabel="Last 7 days" default />
    <ButtonGroupItem value="30" valueLabel="Last 30 days" />
    <ButtonGroupItem value="90" valueLabel="Last 90 days" />
    <ButtonGroupItem value="180" valueLabel="Last 180 days" />
</ButtonGroup>

```sql totals
select day, total_events, active_users
from twillingate.v_product_totals
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
order by day
```

```sql headline
select sum(total_events) as total_events, max(active_users) as peak_dau,
       avg(active_users) as avg_dau
from twillingate.v_product_totals
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
```

<Grid cols=3>
    <BigValue data={headline} value=total_events fmt=num0 title="Total events" />
    <BigValue data={headline} value=peak_dau fmt=num0 title="Peak DAU" />
    <BigValue data={headline} value=avg_dau fmt=num0 title="Avg DAU" />
</Grid>

<Grid cols=2>
    <LineChart data={totals} x=day y=active_users title="Daily active users" yFmt=num0 />
    <LineChart data={totals} x=day y=total_events title="Events per day" yFmt=num0 />
</Grid>

```sql app_versions
-- $app_version rolls up unconditionally, so this needs no declared
-- attribute. Summed across event names: a day's count is how many product
-- events that version fired, whatever they were.
select day, attr_value as app_version, sum(count) as count
from twillingate.v_product_attrs
where project_id = '${params.project}'
  and attr_key = '$app_version'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by day, attr_value
order by day, count desc
```

```sql app_version_summary
-- unique_users and unique_groups are per event and cannot be summed -- one
-- person or one group firing two events would count twice -- so the largest
-- single-event figure is shown, a floor on the true number. unique_groups
-- is NULL for days rolled up before the collector measured it; max() skips
-- those, so a range with no measured day shows an empty cell, not 0.
select attr_value as app_version, sum(count) as total,
       max(unique_users) as min_users,
       max(unique_groups) as min_groups
from twillingate.v_product_attrs
where project_id = '${params.project}'
  and attr_key = '$app_version'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by attr_value
order by total desc
```

{#if app_versions.length > 0}

## App version

Which build the events came from, day by day — a rollout landing shows as one
band taking over, and the versions that never update are the ones that stay.
Users and Groups are the largest counts from any single event, so the true
numbers are at least those; Groups is empty for days rolled up before the
collector measured groups.

<AreaChart data={app_versions} x=day y=count series=app_version title="Events by app version" yFmt=num0 />

<DataTable data={app_version_summary} rows=8 search=true>
    <Column id=app_version title="Version" />
    <Column id=total title="Events" fmt=num0 contentType=colorscale />
    <Column id=min_users title="Users (at least)" fmt=num0 />
    <Column id=min_groups title="Groups (at least)" fmt=num0 />
</DataTable>

{/if}

```sql events
select day, event_name, count, unique_users
from twillingate.v_product_daily
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
order by day
```

```sql event_summary
select event_name, sum(count) as total, max(unique_users) as peak_daily_uniques
from twillingate.v_product_daily
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by event_name order by total desc
```

## Events

<LineChart data={events} x=day y=count series=event_name title="Events by name" yFmt=num0 />

<DataTable data={event_summary} rows=10 search=true>
    <Column id=event_name title="Event" />
    <Column id=total title="Total" fmt=num0 contentType=colorscale />
    <Column id=peak_daily_uniques title="Peak daily uniques" fmt=num0 />
</DataTable>

```sql attr_breakdowns
-- Summed across events: a value's count is how often it appeared on any
-- event that day. unique_users and unique_groups are per event and cannot
-- be summed -- one person or one group firing two events would count twice
-- -- so the largest single-event figure is shown, a floor on the true
-- number. unique_groups is NULL for days rolled up before the collector
-- measured it; max() skips those, so such a day shows an empty cell, not 0.
select attr_key, day, attr_value, sum(count) as count,
       max(unique_users) as min_users,
       max(unique_groups) as min_groups
from twillingate.v_product_attrs
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by attr_key, day, attr_value
order by attr_key, day desc, count desc
```

{#if attr_breakdowns.length > 0}

## Attribute breakdowns

Each attribute's values per day, across all events. Users and Groups are the
largest counts from any single event, so the true numbers are at least
those. Groups is empty for days the collector rolled up before it measured
groups — unknown, not zero.

<DataTable data={attr_breakdowns} rows=20 groupBy=attr_key groupsOpen=false search=true>
    <Column id=attr_key title="Attribute" />
    <Column id=day title="Day" />
    <Column id=attr_value title="Value" />
    <Column id=count title="Count" fmt=num0 contentType=colorscale />
    <Column id=min_users title="Users (at least)" fmt=num0 />
    <Column id=min_groups title="Groups (at least)" fmt=num0 />
</DataTable>

{/if}

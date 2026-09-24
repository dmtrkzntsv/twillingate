# {project_name[0].name} — Users

```sql project_name
select name from twillingate.projects where id = '${params.project}'
```

<ReportNav project={params.project} current="users" />

Per-user rows appear once this project's clients send a `$user_id`: a tag
with `data-identity="identified"` after `identify()`, or a backend that posts
one. A project whose clients send none has nothing here; group reporting
does not need ids, see [Groups](/groups/{params.project}).

<ButtonGroup name=range title="Date range">
    <ButtonGroupItem value="1" valueLabel="Last 1 day" />
    <ButtonGroupItem value="7" valueLabel="Last 7 days" default />
    <ButtonGroupItem value="30" valueLabel="Last 30 days" />
    <ButtonGroupItem value="90" valueLabel="Last 90 days" />
    <ButtonGroupItem value="180" valueLabel="Last 180 days" />
</ButtonGroup>

```sql users_first
-- First day each user appears in the retained history. "New" below means new
-- to that history: someone returning after it has aged out counts as new.
select id, min(day) as first_day
from twillingate.v_identity_daily
where project_id = '${params.project}' and kind = 'user' and id != ''
group by id
```

```sql users_daily
select d.day,
       count(distinct case when d.day = f.first_day then d.id end) as new_users,
       count(distinct case when d.day > f.first_day then d.id end) as returning_users
from twillingate.v_identity_daily d
join ${users_first} f on f.id = d.id
where d.project_id = '${params.project}' and d.kind = 'user' and d.id != ''
  and d.day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
                and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by d.day order by d.day
```

```sql users_totals
select count(distinct d.id) as users,
       count(distinct case when f.first_day = d.day then d.id end) as new_users,
       sum(d.views + d.events) as actions,
       case when count(distinct d.id) > 0
            then sum(d.views + d.events) * 1.0 / count(distinct d.id) else 0 end as per_user
from twillingate.v_identity_daily d
join ${users_first} f on f.id = d.id
where d.project_id = '${params.project}' and d.kind = 'user' and d.id != ''
  and d.day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
                and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
```

```sql users_top
select coalesce(i.name, d.id) as name,
       sum(d.events) as events,
       sum(d.views) as views,
       sum(d.views + d.events) as actions,
       count(distinct d.day) as active_days,
       min(f.first_day) as first_seen,
       max(d.day) as last_seen
from twillingate.v_identity_daily d
join ${users_first} f on f.id = d.id
left join twillingate.identities i
  on i.project_id = d.project_id and i.kind = 'user' and i.id = d.id
where d.project_id = '${params.project}' and d.kind = 'user' and d.id != ''
  and d.day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
                and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by d.id, i.name
order by actions desc limit 100
```

```sql users_kinds
-- Which kinds of activity this project sends. The per-kind columns only
-- earn their place when there is more than one to tell apart.
select sum(views) > 0 as has_views, sum(events) > 0 as has_events,
       (sum(views) > 0)::int + (sum(events) > 0)::int as kinds
from twillingate.v_identity_daily
where project_id = '${params.project}' and kind = 'user' and id != ''
```

<Grid cols=4>
    <BigValue data={users_totals} value=users fmt=num0 title="Active users" />
    <BigValue data={users_totals} value=new_users fmt=num0 title="New users" />
    <BigValue data={users_totals} value=actions fmt=num0 title="Actions" />
    <BigValue data={users_totals} value=per_user fmt=num1 title="Actions per user" />
</Grid>

<BarChart data={users_daily} x=day y={['new_users', 'returning_users']} title="Daily active users, new and returning" yFmt=num0 />

## Most active users

Actions add up every view and custom event the user sent. Names appear once
a client sends `$user_name`.

<DataTable data={users_top} rows=15 search=true>
    <Column id=name title="User" />
    <Column id=actions title="Actions" fmt=num0 contentType=colorscale />
    {#if users_kinds[0].kinds > 1}
        {#if users_kinds[0].has_events}<Column id=events title="Events" fmt=num0 />{/if}
        {#if users_kinds[0].has_views}<Column id=views title="Views" fmt=num0 />{/if}
    {/if}
    <Column id=active_days title="Active days" fmt=num0 />
    <Column id=first_seen title="First seen" />
    <Column id=last_seen title="Last seen" />
</DataTable>

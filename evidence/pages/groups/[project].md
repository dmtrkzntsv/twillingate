# {project_name[0].name} — Groups

```sql project_name
select name from twillingate.projects where id = '${params.project}'
```

Groups need no user ids. `group_id` identifies an organization rather than a
natural person and is stored as sent, so this page fills for any client that
sends `$group_id`.

<ButtonGroup name=range title="Date range">
    <ButtonGroupItem value="1" valueLabel="Last 1 day" />
    <ButtonGroupItem value="7" valueLabel="Last 7 days" default />
    <ButtonGroupItem value="30" valueLabel="Last 30 days" />
    <ButtonGroupItem value="90" valueLabel="Last 90 days" />
    <ButtonGroupItem value="180" valueLabel="Last 180 days" />
</ButtonGroup>

```sql groups_first
-- First day each group appears in the retained history.
select id, min(day) as first_day
from twillingate.v_identity_daily
where project_id = '${params.project}' and kind = 'group' and id != ''
group by id
```

```sql groups_daily
select d.day,
       count(distinct case when d.day = f.first_day then d.id end) as new_groups,
       count(distinct case when d.day > f.first_day then d.id end) as returning_groups
from twillingate.v_identity_daily d
join ${groups_first} f on f.id = d.id
where d.project_id = '${params.project}' and d.kind = 'group' and d.id != ''
  and d.day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
                and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by d.day order by d.day
```

```sql groups_totals
select count(distinct d.id) as groups,
       count(distinct case when f.first_day = d.day then d.id end) as new_groups,
       sum(d.views + d.events) as actions,
       case when count(distinct d.id) > 0
            then sum(d.views + d.events) * 1.0 / count(distinct d.id) else 0 end as per_group
from twillingate.v_identity_daily d
join ${groups_first} f on f.id = d.id
where d.project_id = '${params.project}' and d.kind = 'group' and d.id != ''
  and d.day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
                and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
```

```sql groups_top
-- users is distinct per day, so the range figure is the busiest day's:
-- the same person on two days cannot be told apart from two people.
select coalesce(i.name, d.id) as name,
       sum(d.views + d.events) as actions,
       max(d.users) as peak_users,
       count(distinct d.day) as active_days,
       min(f.first_day) as first_seen,
       max(d.day) as last_seen
from twillingate.v_identity_daily d
join ${groups_first} f on f.id = d.id
left join twillingate.identities i
  on i.project_id = d.project_id and i.kind = 'group' and i.id = d.id
where d.project_id = '${params.project}' and d.kind = 'group' and d.id != ''
  and d.day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
                and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by d.id, i.name
order by actions desc limit 100
```

<Grid cols=4>
    <BigValue data={groups_totals} value=groups fmt=num0 title="Active groups" />
    <BigValue data={groups_totals} value=new_groups fmt=num0 title="New groups" />
    <BigValue data={groups_totals} value=actions fmt=num0 title="Actions" />
    <BigValue data={groups_totals} value=per_group fmt=num1 title="Actions per group" />
</Grid>

<BarChart data={groups_daily} x=day y={['new_groups', 'returning_groups']} title="Daily active groups, new and returning" yFmt=num0 />

## Most active groups

Peak daily users is the most distinct users seen in the group on any one day
of the range; activity sent without a `user_id` counts toward actions only.

<DataTable data={groups_top} rows=15 search=true>
    <Column id=name title="Group" />
    <Column id=actions title="Actions" fmt=num0 contentType=colorscale />
    <Column id=peak_users title="Peak daily users" fmt=num0 />
    <Column id=active_days title="Active days" fmt=num0 />
    <Column id=first_seen title="First seen" />
    <Column id=last_seen title="Last seen" />
</DataTable>

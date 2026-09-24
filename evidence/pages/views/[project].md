# {project_name[0].name} — Views

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

```sql daily
select day, sum(visitors) as visitors, sum(views) as views, sum(sessions) as sessions,
       case when sum(sessions) > 0 then sum(bounces) * 1.0 / sum(sessions) else 0 end as bounce_rate,
       case when sum(sessions) > 0 then sum(duration_sec) * 1.0 / sum(sessions) else 0 end as avg_session_sec
from twillingate.v_views_daily
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by day order by day
```

```sql totals
select sum(visitors) as visitors, sum(views) as views, sum(sessions) as sessions,
       case when sum(sessions) > 0 then sum(bounces) * 1.0 / sum(sessions) else 0 end as bounce_rate,
       case when sum(sessions) > 0 then sum(duration_sec) * 1.0 / sum(sessions) else 0 end as avg_session_sec
from twillingate.v_views_daily
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
```

```sql kinds
select day, kind, visitors
from twillingate.v_views_daily
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
order by day, kind
```

<Grid cols=4>
    <BigValue data={totals} value=visitors fmt=num0 title="Visitors" />
    <BigValue data={totals} value=views fmt=num0 title="Views" />
    <BigValue data={totals} value=bounce_rate fmt=pct1 title="Bounce rate" />
    <BigValue data={totals} value=avg_session_sec fmt=num0 title="Avg session (sec)" />
</Grid>

<LineChart data={daily} x=day y={["visitors","views"]} title="Visitors & views" yFmt=num0 />

<Grid cols=2>
    <AreaChart data={kinds} x=day y=visitors series=kind title="Visitors by kind" yFmt=num0 />
    <LineChart data={daily} x=day y=avg_session_sec yFmt=num0 title="Avg session length (sec)" />
</Grid>

```sql pages
select path, sum(visitors) as visitors, sum(views) as views,
       '/views/${params.project}/page?path=' || path as detail_url
from twillingate.v_views_paths
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by path order by views desc limit 20
```

```sql referrers
select source, sum(visitors) as visitors, sum(views) as views
from twillingate.v_views_referrers
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and source != ''
group by source order by visitors desc limit 20
```

```sql hosts
select host, sum(visitors) as visitors, sum(views) as views
from twillingate.v_views_hosts
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and host != ''
group by host order by views desc limit 20
```

## Pages & referrers

<Grid cols=2>
    <DataTable data={pages} rows=10 title="Top pages">
        <Column id=detail_url title="Path" contentType=link linkLabel=path />
        <Column id=visitors title="Visitors" fmt=num0 contentType=colorscale />
        <Column id=views title="Views" fmt=num0 />
    </DataTable>
    <BarChart data={referrers} x=source y=visitors swapXY=true title="Referrers" yFmt=num0 />
</Grid>

<Grid cols=1>
    <DataTable data={hosts} rows=10 title="Hosts">
        <Column id=host title="Host" />
        <Column id=visitors title="Visitors" fmt=num0 contentType=colorscale />
        <Column id=views title="Views" fmt=num0 />
    </DataTable>
</Grid>

```sql campaigns
select utm_source, utm_medium, utm_campaign, sum(visitors) as visitors, sum(views) as views
from twillingate.v_views_utm
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and utm_source != ''
group by utm_source, utm_medium, utm_campaign order by visitors desc limit 20
```

## Campaigns

<DataTable data={campaigns} rows=10>
    <Column id=utm_source title="Source" />
    <Column id=utm_medium title="Medium" />
    <Column id=utm_campaign title="Campaign" />
    <Column id=visitors title="Visitors" fmt=num0 contentType=colorscale />
    <Column id=views title="Views" fmt=num0 />
</DataTable>

```sql countries
select country, sum(visitors) as visitors from twillingate.v_views_countries
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and country != ''
group by country order by visitors desc limit 20
```

```sql oses
select case when os_version != '' then os || ' ' || os_version else os end as os, sum(visitors) as visitors
from twillingate.v_views_os
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and os != ''
group by 1 order by visitors desc limit 15
```

```sql browsers
select case when browser_version != '' then browser || ' ' || browser_version else browser end as browser, sum(visitors) as visitors
from twillingate.v_views_browsers
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and browser != ''
group by 1 order by visitors desc limit 15
```

```sql devices
select case when device_model != '' then device_model else device end as device, sum(visitors) as visitors
from twillingate.v_views_devices
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and (device != '' or device_model != '')
group by 1 order by visitors desc limit 15
```

```sql displays
select display, sum(visitors) as visitors from twillingate.v_views_displays
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and display != ''
group by display order by visitors desc limit 15
```

## Audience

<Grid cols=2>
    <BarChart data={countries} x=country y=visitors swapXY=true title="Countries" yFmt=num0 />
    <BarChart data={devices} x=device y=visitors swapXY=true title="Devices" yFmt=num0 />
</Grid>

<Grid cols=2>
    <BarChart data={browsers} x=browser y=visitors swapXY=true title="Browsers" yFmt=num0 />
    <BarChart data={oses} x=os y=visitors swapXY=true title="Operating systems" yFmt=num0 />
</Grid>

<BarChart data={displays} x=display y=visitors swapXY=true title="Display resolutions" yFmt=num0 />

```sql consent
select consent, sum(visitors) as visitors, sum(views) as views
from twillingate.v_views_consent
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
group by consent order by visitors desc
```

```sql consent_rate
select case when sum(case when consent in ('given', 'none') then visitors else 0 end) > 0
            then sum(case when consent = 'given' then visitors else 0 end) * 1.0
                 / sum(case when consent in ('given', 'none') then visitors else 0 end)
       end as rate
from twillingate.v_views_consent
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
```

## Consent

Whether the client could keep anything on the device when it sent the view.
`unknown` is every view sent without `$consent`, including all history before
the flag existed; the rate leaves it out.

<Grid cols=2>
    <BigValue data={consent_rate} value=rate fmt=pct1 title="Consent rate (given / (given + none))" />
    <DataTable data={consent} rows=3>
        <Column id=consent />
        <Column id=visitors fmt=num0 />
        <Column id=views fmt=num0 />
    </DataTable>
</Grid>

```sql app_versions
select day, platform || ' ' || app_version as version, visitors
from twillingate.v_views_app_versions
where project_id = '${params.project}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
  and app_version != ''
order by day, version
```

{#if app_versions.length > 0}

## Version adoption

<AreaChart data={app_versions} x=day y=visitors series=version title="Visitors by app version" yFmt=num0 />

{/if}

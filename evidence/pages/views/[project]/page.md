# {browser ? $page.url.searchParams.get('path') : null}

```sql project_name
select name from twillingate.projects where id = '${params.project}'
```

[← back to {project_name[0].name}](/views/{params.project})

<ButtonGroup name=range title="Date range">
    <ButtonGroupItem value="1" valueLabel="Last 1 day" />
    <ButtonGroupItem value="7" valueLabel="Last 7 days" default />
    <ButtonGroupItem value="30" valueLabel="Last 30 days" />
    <ButtonGroupItem value="90" valueLabel="Last 90 days" />
    <ButtonGroupItem value="180" valueLabel="Last 180 days" />
</ButtonGroup>

<!--
  The path comes from the query string, and Evidence interpolates ${...} into
  the SQL text verbatim -- an unescaped value can close the string literal and
  rewrite the predicate. Doubling single quotes keeps it a literal.

  Every read is guarded by `browser`. SvelteKit refuses to expose
  url.searchParams while prerendering, since a prerendered URL has no query,
  and touching it there fails `evidence build` with a 500 on this route --
  which `evidence dev` never shows, because dev does not prerender. The
  prerendered file is a shell; Evidence resolves these queries in the browser
  via DuckDB, so the real path arrives with the first client render.

  The prerender branch names an input nobody sets rather than a literal. With
  the range defaulted, a literal would let the query resolve at build time
  for path '', and Evidence seeds the browser's first run with that result:
  the default range would show nothing until another one was clicked. An
  unset input keeps the query unresolved at build time, so nothing ships.
-->

```sql page_daily
select day, visitors, views
from twillingate.v_views_paths
where project_id = '${params.project}'
  and path = '${browser ? ($page.url.searchParams.get('path') ?? '').replaceAll("'", "''") : inputs.unset_while_prerendering}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
order by day
```

```sql page_totals
select sum(visitors) as visitors, sum(views) as views,
       case when sum(visitors) > 0 then sum(views) * 1.0 / sum(visitors) else 0 end as views_per_visitor
from twillingate.v_views_paths
where project_id = '${params.project}'
  and path = '${browser ? ($page.url.searchParams.get('path') ?? '').replaceAll("'", "''") : inputs.unset_while_prerendering}'
  and day between strftime((now() at time zone 'UTC')::date - interval (${inputs.range} - 1) day, '%Y-%m-%d')
               and strftime((now() at time zone 'UTC')::date, '%Y-%m-%d')
```

<Grid cols=3>
    <BigValue data={page_totals} value=visitors fmt=num0 title="Visitors" />
    <BigValue data={page_totals} value=views fmt=num0 title="Views" />
    <BigValue data={page_totals} value=views_per_visitor fmt=num1 title="Views per visitor" />
</Grid>

<LineChart data={page_daily} x=day y={["visitors","views"]} title="Traffic to this page" yFmt=num0 />

<DataTable data={page_daily} rows=15>
    <Column id=day title="Day" />
    <Column id=visitors title="Visitors" fmt=num0 contentType=colorscale />
    <Column id=views title="Views" fmt=num0 />
</DataTable>

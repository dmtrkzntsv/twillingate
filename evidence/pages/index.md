# Twillingate

```sql active_projects
select id, name, identity
from twillingate.projects
where archived = 0 and id != 0
order by name
```

```sql archived_count
select count(*) as n
from twillingate.projects
where archived = 1 and id != 0
```

## Projects

{#each active_projects as p}

- **{p.name}** ({p.identity}) — [views](/views/{p.id}) · [product](/product/{p.id}) · [users](/users/{p.id}) · [groups](/groups/{p.id}) · [retention](/retention/{p.id})

{/each}

{#if archived_count[0].n > 0}

[Archived projects ({archived_count[0].n})](/archived)

{/if}

# Twillingate

```sql active_projects
select alias, name, identity
from twillingate.projects
where archived = 0 and alias != ''
order by name
```

```sql archived_count
select count(*) as n
from twillingate.projects
where archived = 1 and alias != ''
```

## Projects

{#each active_projects as p}

- **{p.name}** ({p.identity}) — [web](/web/{p.alias}) · [app](/app/{p.alias}) · [product](/product/{p.alias}) · [users](/users/{p.alias}) · [groups](/groups/{p.alias}) · [retention](/retention/{p.alias})

{/each}

{#if archived_count[0].n > 0}

[Archived projects ({archived_count[0].n})](/archived)

{/if}

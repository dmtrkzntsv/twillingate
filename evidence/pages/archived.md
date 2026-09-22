---
sidebar_link: false
---

# Archived projects

```sql archived_projects
select id, name
from twillingate.projects
where archived = 1 and id != 0
order by name
```

[← All projects](/)

{#if archived_projects.length > 0}

{#each archived_projects as p}

- **{p.name}** — [views](/views/{p.id}) · [product](/product/{p.id})

{/each}

{:else}

No archived projects.

{/if}

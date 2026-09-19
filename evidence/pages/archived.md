---
sidebar_link: false
---

# Archived projects

```sql archived_projects
select alias, name
from twillingate.projects
where archived = 1 and alias != ''
order by name
```

[← All projects](/)

{#if archived_projects.length > 0}

{#each archived_projects as p}

- **{p.name}** — [views](/views/{p.alias}) · [product](/product/{p.alias})

{/each}

{:else}

No archived projects.

{/if}

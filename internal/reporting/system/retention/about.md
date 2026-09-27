Cohorts exist for actors identified by a `$user_id` or a stable
`$install_id`. A project whose clients send neither has none: the widgets
below stay empty until they do.

The range selects cohorts by first day: actors first seen inside it,
followed for their first 45 days. A young cohort contributes only the
offsets it has reached, so D45 needs cohorts at least 45 days old.

Cohorts are kept apart by how the actor was identified: **user** (the client
sent a `$user_id`) and **install** (a stable `$install_id`) describe
different populations, and blending their curves would describe neither. An
actor known only by its connection hash is not cohorted at all: that hash
rotates with the salt, so it can never appear in a later cohort.

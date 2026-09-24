<script>
	// The links between one project's reports, so switching from views to
	// product or groups does not mean going back to the project list. Evidence
	// imports anything under components/ into every page on its own.
	export let project;
	export let current;

	const reports = [
		['views', 'Views'],
		['product', 'Product'],
		['users', 'Users'],
		['groups', 'Groups'],
		['retention', 'Retention']
	];
</script>

<nav class="my-4 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
	<a href="/" class="text-base-content-muted hover:underline">All projects</a>
	<span class="text-base-content-muted">/</span>
	{#each reports as [slug, label]}
		{#if slug === current}
			<span class="font-semibold" aria-current="page">{label}</span>
		{:else}
			<a href="/{slug}/{project}" class="text-primary hover:underline">{label}</a>
		{/if}
	{/each}
</nav>

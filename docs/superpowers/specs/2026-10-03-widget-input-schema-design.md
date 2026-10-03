# Widget input schema

Status: draft
Date: 2026-10-03

## Problem

- **An agent learns a widget's props are wrong only by calling.** The
  server already validates props against each component's JSON Schema
  (`contract.props` in `web/src/components/widgets/*.tsx`, written to
  `components.json`, resolved in `internal/reporting/components.go`,
  enforced by `checkProps`). But the input schemas of `add_widget`,
  `update_widget` and `create_dashboard` describe `props` as
  `{"type": "object"}` and `component` as any string, so neither an MCP
  client nor a client generated from `/api/doc` can see what a component
  takes. The schemas are in `list_components` and `reporting_guide`, but
  nothing ties them to the input.
- **The same holds for the rest of a widget's shape.** `source.type`
  has no enum, `width` and `height` no bounds, and nothing says that
  `markdown` takes `md` while every other component takes `sql`.

A dry-run tool was considered and rejected: the server already runs
every check on the real write and refuses it with the reason, so a
separate dry run would add nothing.

## Decisions

- **D1. The tool input schemas carry the widget contract.** For
  `add_widget`, `update_widget` and each item of `create_dashboard`'s
  `widgets`, the inferred input schema is tightened at startup:
  - `component`: `enum` of the component names;
  - `source.type`: `enum` of the registered source types;
  - `width`, `height`: `integer`, `minimum` 1, `maximum` 12;
  - `props` stays `{"type": "object"}`, and an `allOf` carries one rule
    per component: `if {properties: {component: {const: X}}, required:
    [component]} then {properties: {props: X's props schema,
    source: {properties: {type: {enum: X.accepts}}}}}`.
  MCP `tools/list` and `/api/doc` show the same schema, since both read
  `spec.in`.
- **D2. The schema comes from the embedded manifest.** It is built from
  `reporting.Manifest()` through `ParseManifest`, the same bytes the
  service syncs into the database at startup, so it is always this
  build's component set. A component removed from the code is not in the
  enum, which matches the server: it cannot be added either.
- **D3. Props are inlined per component.** Each component's props
  schema sits in its rule's `then`. `$defs` was the first choice, for a
  refusal path naming the component, but the OpenAPI document copies
  the input schema into a request body, where `#/$defs/...` resolves
  against the document root, not the schema. A refusal's path reads
  `/allOf/<n>/then/properties/props/...`; the caller knows which
  component it sent.
- **D4. `update_widget` without `component` keeps a plain object.**
  Its `if` requires `component`; when it is omitted the call changes the
  stored component's props, which the schema cannot know, so `props` is
  any object and the server checks it as today.
- **D5. `reporting` owns the tightening.** The widget contract is
  reporting's domain: `reporting.ConstrainWidget(widget *jsonschema.Schema,
  comps []Component, sourceTypes []string) error` tightens, in place,
  `widget`, an object schema carrying `component`, `props`, `source`,
  `width` and `height` (for `create_dashboard`,
  `properties.widgets.items`). `api` calls it; `spec` gains an optional
  hook that `expose` applies to the inferred input schema before
  registering the tool and before the OpenAPI document reads it.
- **D6. The server's own checks stay.** `checkProps`, `checkSize`, the
  accepts check and the SQL-against-columns check are unchanged. REST
  bodies are not validated against the input schema, so they keep the
  server's refusals; `update_widget` without `component`, and everything
  a schema cannot express (the SQL, its columns, its rows), still rely
  on them.
- **D7. MCP refusals for a schema failure are the SDK's.** The MCP SDK
  validates arguments against the input schema before the handler runs,
  so a bad prop, an unknown component or a width of 13 is refused with
  jsonschema-go's text, for example `validating /allOf/1/then/properties/props/properties/format: enum: pct does not equal any of: [number percent duration]`.
  Wordier than the server's refusals, but it names the field and the
  allowed values.
- **D8. `width: 0` is refused over MCP.** It meant "default"; omitting
  the field still does. The docs already say 1–12. Over REST, 0 keeps
  meaning the default.

## Changes

- `internal/reporting/schema.go` (new): `ConstrainWidget` and its
  helpers.
- `internal/api/expose.go`: an optional `spec` hook,
  `constrain func(in *jsonschema.Schema)`, applied after `schemaFor`.
- `internal/api/ops_reporting.go`: the three tools set the hook;
  `create_dashboard`'s targets `properties.widgets.items`.
- `docs/reporting.md`: one sentence saying that the widget tools' input
  schemas carry each component's props, source types and size bounds.

## Tests

- `ConstrainWidget`: every component's worked examples pass; an unknown
  component, an unknown prop, a prop value outside its enum, `md` on
  `bar`, `sql` on `markdown` and a width of 13 are refused; `props`
  without `component` (the `update_widget` case) passes.
- MCP: `add_widget` with a bad prop is refused before anything is
  written (the dashboard's widget count is unchanged), and `tools/list`
  shows the component enum on all three tools.
- OpenAPI: `/api/doc` carries the component enum and the per-component rules.
- The existing `docs_sync` worked-example test keeps passing through
  `add_widget`, now through the tightened schema too.

## Release note

`feat(api): type widget props per component in the tool input schemas`

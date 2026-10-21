# Views

A view is a screen built from widgets bound to sources. Views live under `views:` in rig.yaml, in a
plugin's manifest, or in their own file (`file: views/board.yaml`).

```yaml
views:
  Board:
    key: B                       # opens it from anywhere
    grid:
      - [{widget: stat, title: Orders/min, source: metrics, query: 'sum(rate(orders_total[1m]))'},
         {widget: table, title: Slow queries, source: db, query: 'select ...', refresh: 30s}]
      - [{widget: terminal, title: Shell}]
```

## Slots

| slot | where it shows |
|---|---|
| `tab` | a top-level screen (the default) |
| `home` | a tile on the Home screen, which appears once any view has this slot |
| `{tab: {key: G}}` | a screen with its own key |
| `{command: usage}` | no tab: `:usage` opens the screen until you leave it |
| `off` | nowhere; a plugin uses it to keep its contract's default view away |

`modes: [software, product]` shows a view only in those UI modes (`V`); a view without `modes:` shows in
every mode but `product`.

A plugin installed for the user (`~/.config/rig/plugins`) shows its views in a project only when
rig.yaml configures it under `plugins:`, its `detect:` matches (`files` globs, `git_remote` in
`.git/config`), or it has neither `detect:` nor a config schema. A contract's default view shows only
for a plugin without views of its own. Screens come first, then the views of plugins serving vcs,
scm, tracker and docs, then the project's own, then the ones a user installed for themselves.

## Layouts

`grid: [[a, b], [c]]` (rows of cells) · `tabs: {Name: widget, ...}` (sub-tabs, ctrl+←→) ·
`split: [a, b]` · `stack: [a, b]` · `box: widget` · `scroll: widget`. A child takes `height:` or
`width:` (rows, columns or a percentage) and `span:` (a weight). `border: false` drops a panel's frame.

## Bindings

`source:` is a source name, a component name (`db`, `metrics`, `runtime`) or `plugins`. With it:
`query:`, `args:`, `refresh:`/`every:` (default 10s), `stream: true` (appends batches, `keep:` rows,
default 5000). Only visible widgets fetch; equal bindings share one request and its cache; errors back
off up to a minute and keep the last frame. `r` refetches.

`transform:` is a list of steps run on the frame:
`filter: "status!=ok"` (`=` `!=` `~` `>` `<` `>=` `<=`, or a bare word) · `sort: -latency` · `limit: 20` ·
`select: [a, b]` · `rename: {a: b}` · `group: {by: svc, agg: {n: count, p: avg(latency)}}` ·
`join: {source: x, query: ..., on: id}` (left join).

## Widgets

| widget | props |
|---|---|
| `table` | `columns: [{name, color, ref, width, hide}]`, `actions`; enter opens a row's ref, `y`/`Y` copy, `/` filters (with no match, enter asks a source that takes `query` for more; esc undoes it, and a changed query is kept only by a saved session). `fold: <parent field>` makes rows with `id` and that field a tree: `←→` space fold, `+ - z` all. `mark: <field>` lets space (ctrl+a: all) pick rows by that column; an action whose args use `${row.x}` then runs on every picked row (esc clears); ←→ scroll columns wider than the table |
| `list` `kv` `tree` | the frame as lines, key/value pairs, a JSON tree |
| `stat` `gauge` `progress` | `value`, `unit`, `min`, `max`, `thresholds`, `color` |
| `bar` `sparkline` `chart` `heatmap` | `series`, `time`, `value`, `unit`, `colors` |
| `text` `markdown` `code` `diff` | `text`, `file`, `field` (one column of the frame), or the frame |
| `logs` | a stream of lines; `stream: true` |
| `tiles` | a scorecard: a tile per row with `title` (or `name`), `value`, `measure`, `target`, `note`, coloured by `status` (ok, warn, err); `label` and `value` name other fields. The TUI shows a table |
| `timeline` | rows with `time`, `title`, `kind`, `status`, `note`, by month; `order: desc` puts the latest first |
| `graph` | a flow: rows with `from` and `to` are edges (`label`, `value`, `status`), rows with `node` describe a node (`label`, `value`, `status`); the web draws it left to right, edges as wide as their value and moving while they carry traffic |
| `waterfall` | spans |
| `calendar` | `time`; the month, with a count on each day that has events |
| `board` | `by` (the column field), `card` (the title field), `key` (shown before it), `columns`; arrows or hjkl move, `/` filters, `<` `>` pick the field cards sort by and `alt+↑↓` its direction; columns that do not fit scroll sideways |
| `form` | `schema`, or `action` (its args), `submit`; `args` are sent with what the form asks |
| `terminal` | `command` (string or list; default `$SHELL`), `dir` (default the project; relative to the view's own folder), `env`, `keep` (runs while hidden). Keys, and clicks when the program asks for the mouse, go to the program; esc (or ctrl+]) leaves |
| `canvas` | drawn by `plugin` through `widget.render` |

`widget: calendar@2` asks for a version; a rig without it, or without the widget, renders `fallback:`
(another widget) instead. Plugins get the host's widget versions at `initialize`.

Colours are theme tokens: `ok`, `warn`, `err`, `info`, `accent`, `muted`, `border`, `fg`, `series1`…

## Actions

```yaml
actions:
  - {key: R, name: retry, action: queue.retry, args: {id: '${row.id}'}}
  - {key: L, name: logs, command: 'logs ${row.service}'}
  - {key: D, name: drop, command: 'db drop ${row.name}', danger: true}
```

`action:` runs through the engine (policy, confirmation, audit). `command:` is a palette line, the CLI's
grammar (`open Jira ${row.issue}` jumps to a view, filtered). `ai:` opens the chat with the prompt typed
in. In args, `${ask}` asks first, `${ask:field}` asks with the row's value filled in, `${pick:field}`
picks from the values the frame has; `danger: true` confirms, and so does an action the plugin marks as danger
(push, discard), in any environment. An action of a plugin not in use is left out, so one key can name
both `gitlab.scm.open` and `github.scm.open`.

A table or board takes `preview:` (a widget, `width: 45%`, `hidden: true`) shown beside it for the
selected row: `${row.x}` in its args and title. `v` shows or hides it, enter on a board card too. A
binding with a `query` arg gets `Q` to edit it.

## Keys

The `rig` preset: `:` palette (`:restart parser`, `:kv get key`, `:open Logs`), `ctrl+k` search,
shift+←→ switch screens, `:plugins` the plugins in use, `.` or right-click a menu of the screen's keys, `ctrl+e` environment, `T` tasks.
`keys: rig` in `.rig/local.yaml` or `~/.config/rig/config.yaml` restores rig's keys (`E`, `N`, `S`,
`ctrl+k` kubeconfig, `ctrl+e` environment details).

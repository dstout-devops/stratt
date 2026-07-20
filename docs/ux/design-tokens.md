# Stratt Design Tokens

**Status:** Design foundation & spec-of-record — the token system the UI is built against (it informed the
greenfield UI rebuild, ADR-0090/0091; `ui/src/index.css` implements the three-tier scheme). **Charter authority:** §3.1
("design tokens as data: all theming via CSS variables; no hardcoded styling in
components") and the [frontend-react rule](../../.claude/rules/frontend-react.md).

This spec defines Stratt's token system: the single source of truth for color,
type, space, and motion. Components consume **semantic** tokens only — never raw
hex, never a primitive step directly. Swapping the primitive layer (a rebrand) or
the theme (light/dark) is then a data change in one file, not a component rewrite.

> The color values below are the **brand-neutral placeholder palette** — the
> validated default from the data-viz method, chosen so the system is correct and
> accessible before a brand exists. When a Stratt brand is chosen, substitute the
> reference-tier hues and re-run `dataviz/scripts/validate_palette.js`; nothing in
> the semantic or component tiers changes.

---

## 1. Token architecture — three tiers

```
Reference (primitive)   →   Semantic (alias)        →   Component / domain
--stratt-blue-450           --color-accent              --log-line-error-bg
--gray-900                  --surface-1                 --plan-diff-add-fg
--space-4                   --text-primary              --finding-critical-fg
```

1. **Reference tokens** name raw values (`--stratt-blue-450: #2a78d6`). Never used
   by a component directly.
2. **Semantic tokens** name *roles* (`--color-accent`, `--surface-2`,
   `--status-critical`). This is the ONLY tier components may reference.
3. **Component / domain tokens** specialize a semantic token for one surface (the
   log viewer, the plan-diff, the graph). Defined where that component lives.

Rule enforced in review (and later by a lint rule on `*.css`/`*.tsx`): a raw hex
or a `--*-<number>` reference token appearing in a component is a defect.

---

## 2. Theming mechanism

All tokens are CSS custom properties on `:root`. Dark theme is a **selected**
theme (its own validated steps, not an automatic filter-invert), applied two ways
so both OS preference and an explicit in-app toggle win correctly:

```css
:root {
  /* light semantic tokens … */
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) { /* dark semantic tokens … */ }
}
:root[data-theme="dark"]  { /* dark semantic tokens … */ }
:root[data-theme="light"] { /* light semantic tokens … */ }
```

Tailwind is build-time only and reads these variables; it never hardcodes a value.

---

## 3. Reference tier (primitives)

### 3.1 Neutrals / chrome

| Token | Light | Dark |
|---|---|---|
| `--page-plane` | `#f9f9f7` | `#0d0d0d` |
| `--surface-1` | `#fcfcfb` | `#1a1a19` |
| `--surface-2` | `#f0efec` | `#242422` |
| `--ink-primary` | `#0b0b0b` | `#ffffff` |
| `--ink-secondary` | `#52514e` | `#c3c2b7` |
| `--ink-muted` | `#898781` | `#898781` |
| `--hairline` | `#e1e0d9` | `#2c2c2a` |
| `--rule` | `#c3c2b7` | `#383835` |
| `--ring` | `rgba(11,11,11,0.10)` | `rgba(255,255,255,0.10)` |

### 3.2 Accent (brand-neutral blue ramp — placeholder)

Sequential blue, 100→700, from the validated palette. `--stratt-blue-450`
(`#2a78d6` light / `#3987e5` dark) is the accent anchor.

```
100 #cde2fb  150 #b7d3f6  200 #9ec5f4  250 #86b6ef  300 #6da7ec
350 #5598e7  400 #3987e5  450 #2a78d6  500 #256abf  550 #1c5cab
600 #184f95  650 #104281  700 #0d366b
```

### 3.3 Status ramp (fixed — never themed per brand)

| Role | Hex | Light contrast | Dark contrast |
|---|---|---|---|
| good | `#0ca30c` | 3.27 | 5.19 |
| warning | `#fab219` | 1.79 | 9.49 |
| serious | `#ec835a` | 2.57 | 6.60 |
| critical | `#d03b3b` | 4.68 | 3.62 |

`warning`/`serious` are sub-3:1 on light **by design**; the mitigation is the
**icon + label** pairing (§6) — a status color never carries meaning alone.

### 3.4 Space, radius, type, motion

- **Space** (4px base): `--space-1: 4px` … `2:8 · 3:12 · 4:16 · 5:24 · 6:32 · 7:48 · 8:64`.
- **Radius:** `--radius-sm: 4px · md: 6px · lg: 10px · full: 9999px`. Data-mark ends
  (bars, log-severity chips) use `sm` per the data-viz mark spec.
- **Type:** system sans only — `system-ui, -apple-system, "Segoe UI", sans-serif`;
  mono for logs/code — `ui-monospace, "SFMono-Regular", "Cascadia Code", monospace`.
  Scale (1.20): `12 · 13 · 14(base) · 16 · 19 · 23 · 28 · 34`. `tabular-nums` on
  table columns, axis ticks, and the log gutter only.
- **Motion:** `--ease-standard: cubic-bezier(.2,0,0,1)`; durations `fast 120ms ·
  base 200ms · slow 320ms`. Respect `prefers-reduced-motion`.
- **Elevation:** two levels only (`--shadow-1` popover, `--shadow-2` dialog); the
  UI leans on hairlines, not shadows.

---

## 4. Semantic tier (the only tier components use)

| Semantic token | Maps to (light → dark) | Used for |
|---|---|---|
| `--color-bg` | `--page-plane` | app background |
| `--color-surface` | `--surface-1` | cards, panels, tables |
| `--color-surface-sunken` | `--surface-2` | log/graph canvas, code wells |
| `--text-primary` | `--ink-primary` | body, values |
| `--text-secondary` | `--ink-secondary` | labels, captions |
| `--text-muted` | `--ink-muted` | axis, timestamps, gutters |
| `--color-border` | `--hairline` | dividers, table rules |
| `--color-accent` | `--stratt-blue-450` | primary action, links, running state |
| `--color-focus` | `--stratt-blue-400` | focus rings (≥2px, always visible) |

---

## 5. Domain tier

### 5.1 Run & Finding state (maps the status ramp to the Named Kinds)

One state palette serves **Run/Step/task-event lifecycle** and **Finding
severity**, so a color means the same thing everywhere the §1.8 descent lands.

| State token | Ramp | Run / Step / task event | Finding severity |
|---|---|---|---|
| `--state-pending` | `--text-muted` | queued / pending | — |
| `--state-running` | `--color-accent` | running (animated) | — |
| `--state-ok` | good | succeeded / changed-ok | compliant / info |
| `--state-attention` | warning | changed-warn / drift-detected | warning |
| `--state-degraded` | serious | partial / retrying | serious |
| `--state-failed` | critical | failed / error | critical |

Every state ships as **dot/chip + icon + label**, never color alone.

### 5.2 Live log viewer (center-of-gravity screen, §3.1)

`--surface-sunken` canvas, mono type, `tabular-nums` gutter. ANSI/severity map:
`--log-info → text-secondary`, `--log-warn → warning`, `--log-error → critical`,
`--log-debug → text-muted`, `--log-success → good`. Row height fixed for
virtualization; the follow-tail affordance uses `--color-accent`.

### 5.3 Plan / drift diff

Diff semantics reuse status, not raw red/green: `--plan-add → good`,
`--plan-destroy → critical`, `--plan-change → warning`, `--plan-noop →
text-muted`. Each line carries a sigil (`+ - ~`) so the diff is legible without
color (colorblind + `forced-colors`).

### 5.4 Graph / View exploration (categorical)

Entity/Relation series draw from the **validated categorical palette**, assigned
in fixed order, never cycled (a 9th series folds into "Other" or small multiples):

```
1 blue #2a78d6/#3987e5   2 aqua #1baf7a/#199e70   3 yellow #eda100/#c98500
4 green #008300/#008300  5 violet #4a3aa7/#9085e9  6 red #e34948/#e66767
7 magenta #e87ba4/#d55181 8 orange #eb6834/#d95926
```

Validated (`dataviz/scripts/validate_palette.js`): light worst-adjacent CVD ΔE
**24.2**; dark **10.3** (floor band → lean on direct labels/texture for ≥4 series).
Status hues are held distinct so a series never impersonates a state.

---

## 6. Compliance checklist

- [ ] No raw hex or `--*-<number>` primitive in any component — semantic tokens only.
- [ ] Every color has a validated light **and** dark step (no auto-invert).
- [ ] State/severity always = color **+ icon + label**.
- [ ] Categorical series assigned in fixed order, never cycled or rank-repainted.
- [ ] Focus ring visible, ≥2px, `--color-focus`.
- [ ] `prefers-reduced-motion` and `forced-colors` honored.
- [ ] Palette re-validated whenever a reference hue changes.

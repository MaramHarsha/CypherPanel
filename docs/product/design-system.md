# CypherPanel — Design system

> Written 2026-09-06, in roadmap Phase 4, **from the components that exist** —
> which is the whole point of writing it now rather than earlier. Every token,
> signature and class string below was read out of `web/src`, not proposed.
> Where the [design canvas](web-ui-design.md) specifies something we have not
> built, it says **NOT BUILT** and says so in §11 rather than describing a
> wish as a fact.
>
> [ui-principles.md](ui-principles.md) is binding and this document does not
> outrank it; it explains *how the code satisfies it*. [web-ui-design.md](web-ui-design.md)
> §2 fixes the aesthetic direction ("Mission Control") and §6 was the component
> plan this inventory replaces. Anything here that contradicts ui-principles.md
> is a bug in this file.

The audience is the person adding a screen tomorrow. The organising claim is
that almost nothing in this UI is a new decision: the surface is a token sheet,
the four states are a wrapper, the status vocabulary is one component, and the
disabled state is a prop. If you are writing a colour, a spinner, or a
"no data" string, you have probably left the system.

---

## 1. The token sheet

**One file: `web/src/styles/globals.css`.** There is no `index.css`, no
`tailwind.config.ts` colour block, no per-component palette. Tailwind v4 means
tokens are ordinary CSS custom properties on `:root`, overridden wholesale on
`.dark`, then re-exported to utilities through `@theme inline { --color-*:
var(--*) }`. A token you cannot find in that file does not exist, and a colour
you write as a hex literal in a component is a bug.

### 1.1 Surfaces — warm paper, never white

| Token | Light | Dark | What it is |
|---|---|---|---|
| `--bg` | `#faf8f4` | `#14120c` | The page ground |
| `--surface` | `#ffffff` | `#1c1914` | A card, a row that lifts off the ground |
| `--raised` | `#f4f1ea` | `#23201a` | Hover fill, a quiet inset |
| `--overlay` | `#ffffff` | `#1c1914` | Dropdown and popover bodies |

Nothing is pure white or pure black, and nothing is cool grey. This is the
single largest carrier of the editorial feel; a `#fff` written by hand reads
as a different product the moment it sits beside `--surface` in dark.

### 1.2 Borders — three weights, and they are not interchangeable

| Token | Light | Dark | Use |
|---|---|---|---|
| `--border` | `#e4dfd5` | `#2a261d` | The row hairline. The base layer sets `* { border-color: var(--border) }`, so a bare `border` class is *already* this. |
| `--border-subtle` | `#eee9df` | `#262218` | The quieter rule *inside* a card, where a full hairline would read as a second table |
| `--border-input` | `#d8d3c8` | `#3a352b` | The line that makes a control look fillable. A field bounded by the row-rule colour stops looking like a field. |
| `--border-strong` | `#16130e` | `#ece7db` | The strongest line in the product |

`--border-strong` **mirrors** rather than dims: ink on paper becomes paper on
ink. It is the 1.5 px rule under the top bar, the rule above a table's first
row, the active tab underline, the outline (`secondary`) pill's edge, and a
focused field's border. If you are reaching for a heavier line than a hairline,
this is the only other line there is.

### 1.3 The text ramp

`--text` `#16130e`/`#ece7db` → `--text-dim` `#514b40`/`#b8b0a0` (readable
secondary body: commit subjects, step lists, rollup counts) → `--text-mid`
`#635e54`/`#a49c8c` (the label grey) → `--text-faint` `#736d61`/`#8d8679`
(eyebrows, timestamps, hints) → `--text-disabled` `#958a75`/`#706a60`.

The ramp below `--text-dim` was **lifted** once the A11y spec's 4.5:1 floor was
measured against it rather than assumed: `--text-faint` was `#8a8375`/`#6f695e`,
which is 3.55:1 light and 3.44:1 dark, and it carries every `Field` hint, every
`.eyebrow` and every dialog footer across roughly 200 call sites. The lift is a
calibration, not a repalette — the greys read the same, they just clear the
floor now.

Three ramp facts that will bite you if you assume symmetry:

- **`--text-disabled` inverts direction.** On light it sits *below* faint; on
  dark it sits *above* it. The ramp is tuned for legibility per theme, not
  mechanically flipped.
- **`--text-disabled` is for controls that cannot be pressed, not for quiet
  text.** It clears the 3:1 disabled floor, not the 4.5:1 text floor, so a
  pending step label, an unselected tile's version or a trace id belongs on
  `--text-faint`. The only live exceptions are `ui/button.tsx`'s disabled
  states and `empty-state.tsx`'s `aria-hidden` glyph.
- **`--disabled-fg` `#817866` holds one value in both themes.** It is the
  disabled pill's *label*, and the pill's fill inverts around it. Keeping one
  value costs the dark pill some headroom (3.63:1, still above the 3:1 disabled
  floor) and is the price of the invariant.

### 1.4 One accent, and it is signal orange

`--accent` `#e8490f`/`#ff6a33`, `--accent-hover` `#b93607`/`#ff8757`,
`--accent-fg` `#ffffff`/`#14120c`.

Reserved for the active top-bar nav item and the single unmissable action on a
screen (sign in, the golden path's next step). The everyday primary action is
**not** orange — it is the ink pill below. That restraint is what makes the
orange mean something; a second orange thing on a screen halves the value of
the first.

### 1.5 The primary pill inverts

`--primary` `#16130e`/`#ece7db`, `--primary-hover` `#2e2a22`/`#fffdf4`,
`--primary-fg` `#faf8f4`/`#14120c`. Ink pill on paper, paper pill on ink.

### 1.6 The status vocabulary is closed — six words

`--status-running` `#2f7d4f`/`#5cbf7f` · `--status-deploying` `#1b62c4`/`#5f9fe8` ·
`--status-stopped` `#736d61`/`#8d8679` · `--status-error` `#d92d0a`/`#ff6a5e` ·
`--status-degraded` `#d9a521`/`#e0b23f` · `--status-unknown` `#736d61`/`#8d8679`.

`stopped` and `unknown` are `--text-faint` by another name and were lifted with
it, for the same reason and at the same time: the status *word* is text, and it
was failing the text floor at 3.55:1 while `error` and `degraded` had already
been given darkened `-text` twins for precisely that.

Each holds 4.5:1 on `--bg` (the dark values were lifted for exactly this).
A seventh word is not a token you add — it is a change to ui-principles §5.
`RedeployPending` exists precisely because "redeploy to apply" wanted to be a
status and was refused a seat.

**Marker colour and text colour are split** where a dot colour is unreadable
as prose:

- `--status-degraded-text` `#9a6700`/`#e0b23f` — amber darkened until it works
  in a sentence. `--status-degraded` stays the dot.
- `--danger` `#c1330a`/`#ff8a7d` and `--danger-hover` `#9c2708`/`#ffa79c` are
  the error **text** colours; `--status-error` stays the marker.

Write status text with `--danger` / `--status-degraded-text`; paint dots with
`--status-error` / `--status-degraded`. Swapping them produces either mud or an
illegible sentence.

### 1.7 Focus is orange in both themes, deliberately

`--focus: #e8490f` — **not overridden in `.dark`.** The ring stays signal
orange because blue is spoken for: `--status-deploying` owns it, and a focused
row that glowed blue would read as a state. Canvas 14g says "2 px orange, 3 px
offset — `:focus-visible` only, identical in dark", and that is one global base
rule (§6.6).

### 1.8 Panes are ink in both themes

`--pane` `#0d0b08` · `--pane-text` `#b8b2a6` · `--pane-faint` `#5d5850` ·
`--pane-dim` `#6f695e` (one step above faint, for 10 px not-yet-reached stages)
· `--pane-border` `#3a3630`. Severity: `--pane-ok` `#4ac26b` ·
`--pane-info` `#58a6ff` · `--pane-warn` `#e0b23f` · `--pane-error` `#ff6a5e`.

**None of these are overridden in `.dark`.** ui-principles §9: a log pane is
already a terminal, and inverting it in light mode makes it harder to read, not
easier. The severity ramp is terminal colour, not status colour — they are
different vocabularies that happen to agree about red.

### 1.9 Toasts are ink in both themes

`--toast` `#16130e` · `--toast-text` `#e9e5dc` · `--toast-faint` `#8a8375` ·
`--toast-dismiss` `#5d5850`. Also never overridden. A toast sits *above* the
page rather than in it, so it does not take the page's theme; it takes its own.

### 1.10 Depth: three depths and one lift, everything else flat

`--depth-modal` `0 20px 50px rgb(0 0 0/.35)` · `--depth-pop` `0 12px 32px
rgb(0 0 0/.25)` · `--depth-card` `0 3px 10px rgb(0 0 0/.08)` · `--depth-lift`
`0 2px 8px rgb(0 0 0/.2)` (the button hover) · `--depth-sheet` `0 -10px 30px
rgb(0 0 0/.3)` — the only shadow that casts upward, for the mobile bottom
sheet. Exposed as `shadow-modal` / `shadow-pop` / `shadow-card` / `shadow-lift`
/ `shadow-sheet`.

In practice the app has almost no shadows, because 1 px borders in three
weights carry the structure (§2 of the design plan: *rules do the work of
boxes*). If you want depth, you probably want a rule.

### 1.11 Radii — three, plus the pill

`--radius-sm` `.25rem` · `--radius-md` `.375rem` · `--radius-lg` `.5rem`.
Dialogs hard-code `rounded-[10px]` (the canvas's card geometry) and pills use
`rounded-full`. That is the entire vocabulary.

### 1.12 Motion — three earned animations

`--animate-sheet-up` (`sheet-up` 220 ms `cubic-bezier(.2,.8,.2,1)`) ·
`--animate-drawer-in` (`drawer-in` 200 ms, same ease) · the un-tokenised
`.animate-status-pulse` (`status-pulse` 1.6 s ease-in-out infinite).

That is the budget. A global `@media (prefers-reduced-motion: reduce)` kills
`status-pulse` outright and collapses every animation and transition to
0.01 ms. Anything else is instant.

### 1.13 Component classes in the sheet

Defined in `@layer components`, so they are part of the token surface even
though they are not tokens:

- `.eyebrow` — mono, `.65625rem` (10.5 px), weight 400, `letter-spacing .12em`,
  uppercase, `--text-faint`.
- `.mono` — mono at a fixed `.78125rem` (12.5 px).
- `.page-title` — 700, `2.125rem`, line-height 1, tracking `-.03em`. Exactly one
  consumer: `PageHeader` at `size="lg"`.
- `.rule-ink` — `border-color: var(--border-strong)`. **Zero call sites** (§6.5).

### 1.14 The base layer

- `* { border-color: var(--border) }` — a bare `border` is the row hairline.
- `html` / `html.dark` set `color-scheme`, so native scrollbars and form
  controls follow.
- `::selection` is accent at 25 %.
- `scrollbar-color` is `--border-strong` on light and `--border-input` on dark
  — a paper-coloured thumb would be the brightest object on an ink page.
- `:focus-visible` (§6.6) and `[data-row]:focus-visible` (§9).

---

## 2. Light and dark

**Light is the root default; dark is a stored opt-in — not a media query.**

- `@custom-variant dark (&:where(.dark, .dark *))` and a `.dark` class on
  `<html>`.
- `main.tsx:12` calls `applyTheme(storedTheme())` **before first render**, so
  there is no flash of the wrong theme.
- `lib/theme.ts` holds *three* preferences (`light` / `dark` / `auto`) resolving
  to *two* themes behind one `localStorage` key, `cypher.theme`, exposed through
  `useSyncExternalStore` (`useTheme`, `useThemePreference`). A module-level
  `matchMedia` listener makes `auto` follow the OS live.

**The token sheet is the mechanism, and that is the whole strategy.** Every
token is redefined *by name* in the `.dark` block, so components carry almost no
`dark:` variants — the only ones in the `ui/` layer are the dialog and drawer
overlays' `dark:bg-black/60`. **If you are writing a `dark:` class, stop and ask
which token is missing.** That is nearly always the real answer.

The deliberate exceptions, all argued above: every `--toast-*` and `--pane-*`
(including the pane severity ramp), `--focus`, and `--disabled-fg` hold one
value in both themes; `--border-strong` mirrors rather than dims;
`--text-disabled` inverts direction in the ramp.

---

## 3. Typography, and the mono-as-identity bet

Two faces, self-hosted via `@fontsource` (the CSP forbids a font CDN, and
inlining as `data:` URIs is banned for the same reason):

- `--font-sans` — `"Instrument Sans Variable"`, `ui-sans-serif`, `system-ui`
- `--font-mono` — `"Fragment Mono"`, `ui-monospace`, `"Cascadia Mono"`

Body is **0.84375rem (13.5 px) / 1.5** with `-webkit-font-smoothing: antialiased`.

**The bet:** mono is not the code font here, it is the *identity* font. Sans is
chrome and prose; mono is promoted to display duty for machine values — ids,
domains, image refs, cron expressions, timestamps, tags, statuses, log lines,
section eyebrows. It encodes the truth that this product's content *is* machine
state, and it is the reason the panel does not read as a generic admin theme.
It is also the direction's one deliberate aesthetic risk, which is why the rule
is "machine values", not "things that look technical". A person's name is sans.
A commit SHA is mono.

**Three delivery mechanisms, and the choice is size-driven:**

1. `.mono` — mono *and* a fixed 12.5 px. Use it where 12.5 px is the right size.
2. `font-mono` (~124 uses) — where you are setting a custom `text-[Npx]`
   alongside it.
3. **Baked into the component.** `Input` / `Textarea` / `Select` are mono by
   *default* (nearly everything typed into this panel is machine state), as are
   `Fact`'s `<dd>`, `HeaderStat`'s 22 px value, `StatusBadge` / `StatusWord`
   (uppercase 10–11 px), `UserAvatar` initials, `EmptyState`'s 30 px glyph,
   `AdvancedSection`'s note, and the step lists in `BlockingProgress` /
   `ProvisioningSteps` (11.5 px / 2.1).

The `.eyebrow` class (§1.13) is the section-label idiom, reached two ways:
the `<Eyebrow>` component (~46 uses, renders an `<h2>`) where a heading is
semantically correct, and the raw `className="eyebrow"` (~26 uses) where the
element must be something else. Retone it by appending a `text-*` class — that
is how the pane and toast surfaces use it (`eyebrow text-pane-faint`).

---

## 4. The four-state page contract

ui-principles §1 says every page and every data region ships Loading / Empty /
Error / Content. `PageState` is how that becomes the path of least resistance
rather than a checklist item someone forgets.

```ts
PageState<T, E = unknown>({
  query: UseQueryResult<T, E>;
  empty?: ReactNode;
  isEmpty?: (data: T) => boolean;        // default: empty array
  loading?: ReactNode;                    // BYPASSES the 200 ms gate
  skeletonColumns?: string;               // default "1fr"
  skeletonRows?: number;                  // default 3
  skeletonDot?: boolean;                  // default false
  children: (data: T) => ReactNode;       // render function over resolved data
})
```

Four things to know:

1. **`children` is a render function** taking resolved data, so the content
   branch cannot accidentally read `undefined`.
2. **The loading branch is gated at 200 ms.** By default `PageState` renders
   nothing for the first 200 ms and then a `SkeletonRows`, wrapped in
   `<div aria-busy>`. Passing `loading` **deliberately bypasses that gate** —
   the route asked for that exact placeholder and is assumed to have gated it
   itself.
3. **`empty` + `isEmpty` split "nothing yet" from "filtered to zero".** These
   are different screens with different verbs (ui-principles §7); `InboxList`
   is the worked example.
4. **The error branch is `QueryError`**, which routes a real API answer to the
   designed page: `NetworkError` → `PlaneOfflinePage`, 403 → `ForbiddenForError`,
   404 → `NotFoundPage`, other 4xx → an inline alert box, 5xx →
   `ServerFaultPage`. Every one of those pages also has an `embedded` variant,
   so a region gets the same design without pretending the whole route died.

```ts
QueryError({ error: unknown; retry?: () => void; retrying?: boolean; lastSyncAt?: number })
```

### The 200 ms rule

`useSkeletonDelay(pending: boolean, delayMs = 200): boolean` is the single
source. It returns `pending && past-delay`; under 200 ms the caller renders
**nothing** — `return null` — never a spinner. A placeholder that appears and
vanishes inside one blink reads as a fault, and a spinner on a page is an
admission that we do not know the shape of what is coming. We do know it.

Routes that supply their own placeholder call the hook themselves:

```tsx
const showSkeleton = useSkeletonDelay(q.isPending);
<PageState query={q} loading={showSkeleton ? <SkeletonRows columns={GRID} /> : null}>
```

(see `routes/_app/projects/$projectId/databases/$dbId/backups.tsx:294-299` and
`routes/_app/projects/$projectId/settings/index.tsx:86-90`).

---

## 5. The components

### 5.1 Primitives — `components/ui/`

**`Button`** — `forwardRef<HTMLButtonElement, ButtonProps>`;
`ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> & VariantProps`:

```ts
variant?: "primary" | "accent" | "secondary" | "ghost" | "danger"   // default "secondary"
size?: "sm" | "md" | "lg"                                            // default "md"
disabledReason?: string
```

Sizes: `sm` = `h-7 px-3 text-xs`, `md` = `h-[34px] px-[18px] text-[12.5px]`
(canvas 10b's geometry), `lg` = `h-10 px-5 text-[13px]`. Hover *lifts*
(`shadow-lift`) and never changes size.

**`disabledReason` is the real disable API.** It sets `aria-disabled` — not
the native `disabled`, which would drop the control from the tab order and stop
the tooltip from ever opening — wraps the button in a `Tooltip`, and swallows
the click (`preventDefault` + `stopPropagation`, which also cancels a form's
implicit submit). The native `disabled` prop is honoured only when there is no
`disabledReason`. Canvas 14g: the reason lives in a tooltip, never in contrast
alone. **A disabled button with no reason is an unfinished button.**

**`ActionButton`** — the six-state action button (canvas 10b/13al). Idle,
hover and disabled belong to `Button`, because every button has them; busy,
success and failed belong here, because they belong to an action with a
lifecycle.

```ts
ActionButtonProps extends Omit<ButtonProps, "children"> & {
  state?: ActionState;          // default "idle" — NOT `busy`/`loading`/`isPending`
  children: ReactNode;          // the IDLE label
  busyLabel?: ReactNode;        // "Working…"
  successLabel?: ReactNode;     // "Done"
  failedLabel?: ReactNode;      // "Failed — retry"
}
type ActionState = "idle" | "busy" | "success" | "failed";
useAction(run: () => Promise<unknown>, successHoldMs = 2000): { state: ActionState; trigger: () => Promise<void> }
useMutationActionState(mutation: { status: "idle"|"pending"|"success"|"error" }, successHoldMs = 2000): ActionState
```

Success and failed **override** the variant to ghost plus status colours;
success holds 2 s then returns to idle. Width is reserved by stacking all four
labels in one grid cell (`grid place-items-center`, invisible siblings) so
nothing jumps. Under `motion-reduce` the spinner swaps to a static `▸` — a
frozen ring reads as a fault, a glyph reads as in-progress.

**It renders a fragment** (the `Button` plus an `sr-only aria-live` span), so
it **cannot be a Radix `asChild` trigger**. That is exactly why
`ConfirmRollback` exists in controlled `open`/`onOpenChange` form.

**`Field`** — the label/hint/error wrapper.

```ts
Field({ label: string; qualifier?: ReactNode; hint?: string; error?: string;
        children: (id: string, describedBy: string | undefined) => ReactNode;
        className?: string })
```

**The child is a render function, not a node:**

```tsx
<Field label="Name" hint="Lowercase, no spaces">
  {(id, describedBy) => <Input id={id} aria-describedby={describedBy} />}
</Field>
```

`describedBy` resolves to the error id when `error` is set, else the hint id,
else `undefined` — so the field never points at a description that is not
there. Label is 12 px/600 with the qualifier in `--text-faint` on the same
line; the error `<p>` is `aria-live="polite"`.

**`Input` / `Textarea` / `Select`** — three `forwardRef`s over the native
elements with **no extra props** (just `className` merged into a shared base).
All three are `font-mono text-[13px]` by default. Border is `--border-input` at
rest; focus-visible deepens to `--border-strong` plus a 1 px ring and
`focus-visible:outline-none` — they are one of the two sanctioned exceptions to
the global ring, because they draw their own indicator (§6.6). `Input` is `h-9`;
`Textarea` `min-h-20 py-2`; `Select` `h-9 pr-8 cursor-pointer`.

**`DialogContent`** — the dialog masthead is the component's job, not yours.

```ts
DialogContent({ title: string; description?: string; eyebrow?: ReactNode;
                size?: "md" | "form" | "alert";   // md=420px, form=560px+22px/700 title+px-7, alert=430px
                hideClose?: boolean; hideTitle?: boolean; children: ReactNode }
              & ComponentPropsWithoutRef<Radix Dialog.Content>)
```

`title` is a **required string prop, not a child**. `eyebrow` paints a
`.eyebrow` mono line above it. `hideClose` drops the ✕, for work that cannot
honestly be dismissed. `hideTitle` keeps the accessible name but paints nothing
*and* forces the description `sr-only` — the ⌘K palette and the shortcuts
overlay use this to draw their own heads. The card is a bounded flex column:
`max-h-[calc(100dvh-2rem)]`, masthead and footer fixed, body scrolls.
`Dialog`, `DialogTrigger`, `DialogClose` are re-exported from Radix unchanged.

**`Drawer`** — prefer this over a modal (ui-principles §4).

```ts
Drawer({ open: boolean; onOpenChange: (open: boolean) => void;
         title: ReactNode;      // rendered, aria-hidden
         label: string;         // the sr-only accessible name
         description?: string; children: ReactNode;
         wide?: boolean;        // max-w-2xl vs max-w-lg
         tone?: "paper" | "ink" })   // default "paper"
```

`title` and `label` are **separate required props** because the visible title
is often a composed node (a name plus a status pill) that reads badly as an
accessible name. With no `description` it explicitly passes
`aria-describedby: undefined` to tell Radix the omission is deliberate rather
than forgotten. `tone="ink"` makes the panel `bg-toast`/`text-toast-text` in
*both* themes and adds `h-[85dvh] sm:h-auto` so an inner log pane can scroll.
Below `sm` it is a bottom sheet with a grab handle (`animate-sheet-up`); from
`sm` a right-hand column (`animate-drawer-in`).

**`Skeleton` family** — `Skeleton({ className?, style? })` (`animate-pulse
rounded bg-border`, `aria-hidden`), plus:

```ts
useSkeletonDelay(pending: boolean, delayMs = 200): boolean
SkeletonRows({ columns: string; rows = 3; dot = true; className? })     // columns is REQUIRED
SkeletonCards({ count = 3; columns = "sm:grid-cols-2 xl:grid-cols-3"; dot = false; className? })
SkeletonForm({ fields = 4; columns: 1 | 2 = 2; className? })
```

`SkeletonRows.columns` is a required grid-template mirroring the real table,
because a skeleton whose columns do not match causes the layout shift it exists
to prevent. Rows fade 1 / .7 / .4, floored at .25; the first row takes
`border-t-[1.5px] border-t-border-strong` like the real table; ragged widths
come from a fixed `WIDTHS` table so they do not shimmer between renders;
**status dots stay `bg-border` grey** — colouring them optimistically would
claim health nobody observed. Everything is `aria-hidden`.

**`Dropdown`** — `Dropdown` and `DropdownTrigger` are bare Radix re-exports.
`DropdownContent` portals with `sideOffset={4}`, `rounded-md border bg-overlay
p-1 shadow-pop`. `DropdownItem` is 13 px with `data-[highlighted]:bg-raised`.
`DropdownSeparator` is `my-1 h-px bg-border`. No custom props anywhere.

**`Tabs`** — `Tabs` and `TabsContent` are bare Radix re-exports. `TabsList` is
`-mb-px flex gap-5 overflow-x-auto border-b`; **the negative margin lives on the
list, never on a trigger**, or the `overflow-x` parent grows a vertical
scrollbar. `TabsTrigger` uses an **ink** underline
(`data-[state=active]:border-border-strong`) — the orange underline belongs to
the top-bar nav and nothing else.

**`Tooltip`** — `TooltipProvider` is a bare re-export;
`Tooltip({ content: ReactNode; children: ReactNode })` has no side/align/delay
props at all. `delayDuration` is hard-coded to 300 and the trigger is always
`asChild`. One tooltip behaviour, product-wide.

**`BlockingProgress`** — the honest blocking popup.

```ts
BlockingProgress({ open: boolean; title: string; detail: string;
                   steps: ProgressStep[];
                   progress?: number;         // 0–1; OMIT when there is no honest measure
                   noCancelReason: string;    // REQUIRED
                   onOpenChange?: (open: boolean) => void })
type StepState = "done" | "active" | "pending";
interface ProgressStep { label: string; state: StepState }
```

`noCancelReason` is required because the design forbids a progress popup that
offers no cancel without saying why. It renders `DialogContent size="alert"
hideClose` with a 3 px red top rule and `preventDefault` on both
`onEscapeKeyDown` and `onPointerDownOutside`. Omit `progress` rather than
inventing a percentage — an invented bar is a lie with a UI.

### 5.2 Product components — `components/`

Grouped by when you would reach for them.

**Page frame**

| Component | Signature | Reach for it when |
|---|---|---|
| `PageHeader` | `{ title: ReactNode; badge?: ReactNode; actions?: ReactNode; hint?: string; below?: ReactNode; size?: "lg"\|"sm" = "lg"; className? }` | Any route's masthead. **There is no `crumbs` prop** — it reads `useCrumbsValue()` from context and the page declares its trail with `useCrumbs()`. Padding is conditional on `below` (34 / 30 / 26 px top). Also exports `HeaderStat({ value, label, tone?: "default"\|"error" })` and `PageBody({ children, className? })`. |
| `Breadcrumbs` | `{ crumbs: Crumb[]; className? }`; `interface Crumb { label: string; to?: string }` | Rendered as the page's mono uppercase eyebrow/dateline with the current resource in the accent. Normally you do not call this — you call `useCrumbs()`. |
| `Eyebrow` | `{ children: ReactNode; className? }` | A section label **where an `<h2>` is semantically right**. It renders one. Where a heading would be wrong, use `className="eyebrow"` on the correct element instead. |
| `FactCard` | `{ title: string; children: ReactNode; className?; actions?: ReactNode }` and `Fact({ label: string; children: ReactNode })` | The overview idiom: a `<section>` with its own `.eyebrow` `<h2>` and a `<dl>`. `Fact` auto-derives a `title` tooltip when children is a string or number, so a truncated value stays readable; it stacks below `sm` with the value on the left. |
| `InlineHint` | `{ children: ReactNode }` | ui-principles §11's one plain-language line under a technical field. A `<p>` at `text-xs`/`--text-mid`. No `className` — the hint looks the same everywhere on purpose. |
| `AdvancedSection` | `{ children: ReactNode; label = "Advanced"; note?: string }` | Everything with a working default (ui-principles §6). Uncontrolled; `note` shows only while collapsed, right-aligned in mono 10.5 px. |
| `EmptyState` | `{ title: string; hint?: string; action?: ReactNode; glyph?: ReactNode = "▢"; emphasis?: boolean; className? }` | Any empty region. **It draws no card and no dashed rule of its own** — the frame belongs to whatever contains it. `emphasis` is the only thing that adds a border, an accent hairline with no fill, reserved for golden-path steps. `glyph` is a 30 px mono Unicode mark, never an icon. |

**Status and progress**

| Component | Signature | Notes |
|---|---|---|
| `StatusBadge` | `{ status: string \| undefined \| null; className? }` | Dot + mono uppercase word, `aria-live="polite"`. The default rendering of status anywhere. |
| `StatusDot` | `+ decorative?` | `role="img"` with an `aria-label` by default. Pass `decorative` where the status word is already beside it — otherwise a screen reader reads the state twice. `StatusBadge` sets it on its own dot. |
| `StatusWord` | `{ status; children?: ReactNode; className? }` | Bare word; `children` override the text so a rollup count can ride along. |
| `StatusPill` | `{ status; children: ReactNode; className? }` | `children` is **required** here, unlike `StatusWord`. |
| | `type Status = "running"\|"deploying"\|"stopped"\|"error"\|"degraded"\|"unknown"`; `normalizeStatus(s)` | **Every status string entering the UI goes through `normalizeStatus`.** That is what keeps the vocabulary closed at six words. |
| `PipelineStages` | `{ status: DeployStatus; detail?: string; className?; tone?: "paper"\|"ink" = "paper" }` | `build → distribute → rollout → serving`. Also exports `failedStage(detail)` and `stageWord(status)`, reused by `deploy-toast`. |
| `ProvisioningSteps` | `{ steps: DbStep[]; progress: number; failed?: boolean; detail?: string; label: string; compact?: boolean; className? }` | Database provisioning. `type DbStepState = "done"\|"active"\|"pending"\|"failed"`; `provisioningSteps(status, engineLabel, serverName): Provisioning`. |
| `RouteStatus` | `{ app: Application; className? }` | Takes the whole `Application`. Internally a closed `Tone = "ok"\|"attention"\|"error"\|"unknown"`. |
| `RedeployPending` | `{ className?; title? }` | A fixed-text amber badge, "redeploy to apply". Deliberately **not** a link and **not** a seventh status word. |
| `SSEBanner` | `{ status: SSEStatus; tone?: "paper"\|"ink" = "paper" }` | ui-principles §10's "reconnecting" banner. Returns `null` when status is `"open"`. On ink it keeps the *undarkened* `--status-degraded`, because `--status-degraded-text` is unreadable on a near-black pane. |

**Actions and confirmation**

| Component | Signature | Notes |
|---|---|---|
| `ConfirmDestructive` | `{ trigger: ReactNode; title: string; blastRadius: string \| string[]; lead = "This permanently removes:"; confirmName?: string; actionLabel: string; onConfirm: () => void; pending?: boolean; pendingLabel = "Working…" }` | ui-principles §2. `blastRadius` accepts a bare string or a list; `confirmName` arms the action only once typed exactly. Open state is internal, trigger-driven. |
| `ConfirmRollback` | `{ trigger?: ReactNode; open?: boolean; onOpenChange?: (open: boolean) => void; appName: string; now: RevisionSummary; target: RevisionSummary; onConfirm: () => void; pending?: boolean }`; `interface RevisionSummary { rev: string; detail?: string; served?: string }` | **Dual-mode**: omit `trigger` and drive it with `open`/`onOpenChange`, which is what a row's `ActionButton` must do (§5.1 — `ActionButton` renders a fragment and cannot be a Radix trigger). |
| `CopyButton` / `CopyField` / `SecretField` | `CopyButton({ value: string; label: string })`, `CopyField({ value; className?; mono = true })`, `SecretField({ value; className? })` | ui-principles §6: every generated value gets a copy button. `CopyButton`'s `aria-label` stays the label and the outcome goes to an `sr-only role="status"` — and a *failed* copy is admitted rather than swallowed. `SecretField` is reveal/hide plus copy, dots capped at 24. |

**Forms**

| Component | Signature |
|---|---|
| `ArgvInput` | `{ value: string[]; onChange: (value: string[]) => void; labelledBy?: string }` — ADR-011: an argv list, never a shell-string textbox |
| `CronField` | `{ value: string; onChange: (value: string) => void; id?: string; describedBy?: string; viewerZone?: string }` — the `id`/`describedBy` pair is exactly what `Field`'s render child hands it. Also exports `type CronPreview = { ok: true; runs: Date[] } \| { ok: false; error: string }` and `cronNextRuns(expr, count)` |
| `BuildStrategyField` | `{ value: AppBuildKind; onChange: (kind) => void; dockerfilePath: string; context: string; eyebrow?: ReactNode; className? }`; also exports `radioArrowTarget(key, index, count): number \| null` — the arrow-key model for the radio group |
| `DomainField` | `{ applicationId: string; value: string; onChange: (next: string) => void }`; also `splitDomain(domain, zones)` |
| `VolumesEditor` | `{ app: Application; projectId: string }` |
| `ServerPublicAddress` | `{ serverId: string; value: string }` |

**Dialogs (create and connect)**

| Component | Signature |
|---|---|
| `CreateProjectDialog` | `()` — no props |
| `NewAppDialog` | `{ envId; projectId; projectName; envName; primary? }` — `primary` only chooses the trigger pill's variant |
| `NewDatabaseDialog` | same shape; also exports `ENGINES: Record<DatabaseEngine, { label; versions; port }>` and `engineLabel(engine, version)` |
| `NewComposeStackDialog` | same shape |
| `JoinServerFirstDialog` | `{ trigger: ReactNode; resource: "application" \| "database" \| "compose stack" }` — the stand-in that hangs off the same pill the real create dialog would use, so the golden path never dead-ends |
| `InviteMemberDialog` | `{ team: Team }` — takes the whole `Team`, not an id |
| `RequestAccessDialog` | `{ open; onOpenChange; teamId: string; teamName: string \| undefined; held: string \| undefined; role: Requestable; owners: string[]; action: string }` — note `teamName` and `held` are **required props typed `\| undefined`**, not optional: the caller must decide, not forget |
| `ReportIssueDialog` | `{ open; onOpenChange; error: unknown }` |
| `ConnectionDialog` | `{ open; onOpenChange; trigger; title; description?; eyebrow?; onSubmit; children; error?; result?; note?; test: ConnectionAction; primary: ConnectionAction & { type?: "submit"\|"button" } }`. Supporting: `interface ConnectionTestResult { tone: "passed"\|"sent"\|"failed"; message: string }`; `interface ConnectionAction { label; state: ActionState; busyLabel; successLabel?; failedLabel?; disabledReason?; onClick? }`; `TestResultBanner({ result })`; `ChoiceChip({ selected; onToggle; children; className? })` (an `aria-pressed` toggle with a ✓ suffix); `NotifierConnectionDialog({ projectId; trigger; notifier? })` — `notifier` present means edit, absent means create |

**Logs, deployments, databases**

| Component | Signature | Notes |
|---|---|---|
| `LogViewer` | `{ url: string; live = false; className? }` | Replay-then-tail, autoscroll with opt-out, wrap toggle. **`live` is caller-declared, not inferred from the transport** — cypherd never closes a replay stream, so the SSE state says "open" for a build that finished yesterday. Pane is ink in both themes; renders `SSEBanner tone="ink"` and an `.eyebrow text-pane-faint` "log" label. |
| `toastDeployment` | `(dep: Deployment, opts: DeployToastOptions): void`; `interface DeployToastOptions { kind: "deploy"\|"rollback"; projectId: string; appId: string }` | No rendered export. It opens a working toast whose title is a live `<DeploymentWatch>` keyed by `deployment-${dep.id}`, so calling it twice **morphs one toast** instead of stacking two. |
| `DatabaseRestoreWatch` | `{ projectId: string; dbId: string }` | Also exports `useRestoreInFlight(dbId)`, `RESTORE_NO_CANCEL` (the string handed to `BlockingProgress.noCancelReason`), `formatBytes(n)`, `restoreStepLabel(r)`. |
| `DomainLink` / `HeaderDomain` | `{ applicationId: string; domain: string; https: boolean }` | Identical signatures, different chrome. Both **refuse to link an unverified domain** when DNS enforcement is on. |
| `DomainVerification` | `{ applicationId: string }` | |

**Shell and global**

| Component | Signature | Notes |
|---|---|---|
| `CommandPalette` | `()` — no props | Mounted once in the shell. Also exports `openCommandPalette(): void`, a window-event trigger, so callers never hold the palette's state. That is how the 404 page opens it without faking a keystroke. |
| `ShortcutsOverlay` | `{ open; onOpenChange }`; also exports `SHORTCUTS: readonly (readonly [string, string])[]` | The `?` overlay. **The list is the contract**: a key printed here must work, and a key that works must be printed here. |
| `InboxBell` | `()` — no props | Chrome, not navigation: it opens a panel in place and leads nowhere, which is why it sits in the top bar's control cluster rather than becoming a fifth nav item. |
| `InboxList` | `{ filters: InboxFilters; limit: number; layout: "panel"\|"page"; onClearFilters; onMore?; onNavigate?; rowsRef?: (el) => void; footer?: (page: { shown: number; more: boolean }) => ReactNode }` | `footer` is a render function fed the current page counts; `rowsRef` is the attach point for the j/k row model. Supporting: `INBOX_MAX = 100`, `KindFilter`, `SeverityFilter`, `InboxFilters`, `NO_FILTERS`, `isFiltered(f)`, `useInboxRefresh()`, `badgeLabel(n)`, `CountPill`, `MarkAllRead`. |
| `UserAvatar` | `{ userId?; name?; email?; className?; textClassName? }` | **Sizing deliberately lives with the caller** (a 22 px chip and a 56 px profile header are the same component). Falls back to mono uppercase initials on a `--primary` fill. Also `avatarQueryKey`, `useAvatar`, `initialsFor`. |
| `QRCode` | `{ value: string; size = 176; label? }` | Inline SVG drawn client-side (one path for all dark modules), four-module quiet zone in the viewBox. |
| `FirstLoginNotice` | `{ first: FirstLogin; templateName: string; onContinue: () => void }` | |
| `ResourceGone` | `{ kind: string; error: unknown; backTo: string; backLabel: string; name? }` | `kind` is a glossary noun, lower case. Falls back to the id in the route params when `name` is absent. |
| `error-page.tsx` | see §7.3 | The five designed error pages plus `ErrorForRoute` and the role helpers. |

### 5.3 Components the design plan named that never became components

`web-ui-design.md` §6 planned `ResourceTable`, `RoleGate` and `EnvSwitcher`.
They do not exist, and the reason each is absent is worth knowing before you
write one:

- **`ResourceTable`** — the tables differ enough per resource that a shared
  component would be all props. What is actually shared is smaller and did get
  extracted: the grid string per route, `SkeletonRows(columns)`, `data-row`
  (§9), and the card shell (§6.4).
- **`RoleGate`** — role gating turned out to be `atLeast()` from `lib/roles.ts`
  plus `Button`'s `disabledReason`, which is strictly better: the affordance
  stays visible and *names the rank it needs*, which is what ui-principles asked
  for. A gate that removes the control cannot explain itself.
- **`EnvSwitcher`** — lives inline in the project route.

Do not resurrect them speculatively. If a second consumer appears, extract then.

---

## 6. The recurring patterns

These are the things that are copied rather than imported, and knowing them is
most of what "matching the codebase" means.

### 6.1 The row-action class string

Not a shared module — a `const rowAction` re-declared per route file. The
canonical form appears verbatim in four files
(`routes/_app/settings/index.tsx:52`, `registries.tsx:43`,
`deploy-keys.tsx:39`, `backup-targets.tsx:37`):

```
"shrink-0 text-[12px] font-medium hover:underline disabled:no-underline disabled:opacity-50"
```

Colour is applied at the call site: `cn(rowAction, "text-danger")` for
destructive, `"text-text-mid"` / `"text-text-dim"` otherwise. When the action is
a `Button` it also gets `"h-auto px-0 hover:bg-transparent"` to strip the pill.

`routes/_app/settings/profile.tsx:525` has a **divergent copy** with the colour
baked in and no disabled handling. **Copy the four-file form, not profile's.**

### 6.2 Mono vs sans

See §3. The short rule: sans is chrome and prose, mono is machine values.

### 6.3 Skeleton content rules

Bars are `bg-border` with sub-bars in `bg-border-subtle`. Rows fade 1 / .7 / .4
floored at .25. The first row takes `border-t-[1.5px] border-t-border-strong`
like the real table. Ragged widths come from a fixed `WIDTHS` table. Status dots
are always neutral `bg-border`. Everything is `aria-hidden`. And nothing renders
before 200 ms (§4).

### 6.4 The card shell

**There is no `Card` component.** The literal string

```
rounded-lg border border-border bg-surface
```

appears ~46 times inline, with `p-4.5` in `FactCard`, `SkeletonCards` and
`AdvancedSection`. This is deliberate: a `Card` component would invite a
`variant` prop, and the design's position is that structure comes from rules,
not from nested boxes (ui-principles §4 caps visual nesting at 3).

### 6.5 The ink rule is spelled inline

`.rule-ink` is defined in `globals.css` and has **zero call sites**. The
strongest line is instead written out as `border-t-[1.5px]
border-t-border-strong` (6 uses, e.g. `SkeletonRows`' first row) or
`border-[1.5px] border-border-strong` (27 total 1.5 px border uses — the
`secondary` pill, the ink drawer's left edge, the active tab). Write the
utilities; the class is vestigial. (`.page-title` is nearly the same story:
exactly one consumer, `PageHeader` at `size="lg"`.)

### 6.6 The focus ring is one global rule

```css
:focus-visible { outline: 2px solid var(--focus); outline-offset: 3px }
[data-row]:focus-visible { outline-offset: -2px }
```

Orange in both themes because `--focus` is never overridden (§1.7). A
j/k-navigable row flips to an inset offset so its ring sits inside its own box
rather than overlapping the rows above and below.

**Nothing may replace this with `outline-none` unless it draws its own
indicator.** The two sanctioned exceptions are `Input`/`Textarea`/`Select` (ink
border plus a 1 px ring) and the ⌘K palette row.

### 6.7 `tone="paper" | "ink"` is a convention, not a one-off

`Drawer`, `PipelineStages` and `SSEBanner` all take it, and it always means
"this surface is a log/terminal context that stays ink in both themes". Its
consequence is **colour substitution, not just a background**: `SSEBanner` swaps
`--status-degraded-text` for the undarkened `--status-degraded` on ink, and
`Drawer tone="ink"` also changes its height strategy (`h-[85dvh] sm:h-auto`) so
an inner pane can scroll. If you add a fourth consumer, take the prop name and
the substitution behaviour with it.

### 6.8 Colour is never the only signal

Status markers differ in **shape**: `running` / `degraded` / `stopped` are
`rounded-full`, `error` is `rounded-[2px]` (square), `unknown` is a hollow ring,
`deploying` wears `.animate-status-pulse`. The same rule runs through the toast
dots (round = ok, square = error) and through every step list, where the
✓ / ▸ / ○ / ✕ glyph is `aria-hidden` and paired with an `sr-only` word —
"Done:" / "In progress:" / "Waiting:" / "Failed:" — declared identically in
`ui/blocking-progress.tsx` and `db-provisioning-steps.tsx`.

This is the rule that makes the status system survive a colour-blind operator
and a phone in sunlight, which is the actual use case (ui-principles §4:
scannable from across the room).

### 6.9 Disabled controls name their reason

See `Button.disabledReason` (§5.1). The disabled *look* is a **filled paper-grey
pill** (`bg-border-subtle` / `text-disabled-fg` / `border-transparent`), not a
faded live one: "unavailable" should look like a different object, not a dimmed
version of the thing you wanted.

That OFF class string is written out in full in `button.tsx` rather than
assembled from a template literal, **because Tailwind extracts class names by
scanning source text** — a variant built by string concatenation is never
generated. It is also scoped `:not([aria-busy=true])` so `ActionButton`'s
inert-while-busy pill keeps its own colour at 75 % opacity.

### 6.10 Toasts

`lib/toast.tsx` is the **only** sonner surface in the app (the `<Toaster>` in
`routes/__root.tsx` aside), and the Toaster is `unstyled` on purpose so a stray
raw sonner call renders as bare text and is caught in review.

```ts
ToastBody = { title: ReactNode; detail?: ReactNode;
              actions?: { label: string; onClick: () => void }[];
              details?: string }   // rendered behind a `details ▾` expander

toastSuccess(copy: ToastBody | string, id?)   // 5000 ms, round --pane-ok dot
toastError(copy: ToastBody, id?)              // Infinity, SQUARE --pane-error dot, must carry a next step
toastWorking(copy, id?)                       // Infinity, spinner mark, NO dismiss button
toastThrough(promise, { working, success: (v) => Copy, error: (err) => ToastBody })
toastFailed(title, err, { retry?, actions?, id? })
```

Four shapes, each an argument:

- **Success leaves after 5 s.** It is confirmation, not information.
- **Errors stay until dismissed and must carry a next step** (canvas 13am, and
  ui-principles §1: an error offers its remedy).
- **Working has no dismiss button** — dismissing it would imply the work
  stopped, and it has not.
- **`toastThrough` threads one id** so working *morphs in place* into success or
  error rather than stacking a second card.

**`toastFailed` is what every mutation's `onError` should use.** It derives the
detail line and whether a Retry is offered *from the error*: `NetworkError` →
retry; 5xx → Retry + Copy details + the fault bundle; 403/404 → the fix in
words, no retry (retrying a 403 is theatre); 429/408 → retry; other 4xx → the
server's own sentence, which the API writes for humans.

Geometry: 360 px (`max-w-[calc(100vw-2rem)]`), 10 px radius, `bg-toast` /
`text-toast-text` — **ink in both themes** — `0 12px 32px` shadow. The Toaster
is bottom-right, gap 10, `--width: 360px`, and `expand` — never collapse an
error behind a success.

Toasts are never load-bearing (ui-principles §3): everything a toast says is
also in the resource's status or the inbox.

### 6.11 Breadcrumbs come from context

Pages declare `useCrumbs([{ label, to }, …])` (`lib/crumbs.tsx`) and
`PageHeader` reads `useCrumbsValue()`. There is no `crumbs` prop, and the trail
clears itself on unmount. This is why a nested route cannot forget its parent's
trail.

---

## 7. The states, with the canvas copy

The copy below is the canvas's and the code's. It is quoted because tone is a
design decision: these pages are calm, they name the fix, and they never end in
a shrug.

### 7.1 Loading

Skeletons matching the final layout, gated at 200 ms (§4). No page spinners.
Canvas 10e: *"Rows mirror the real layout (name, rollup chip, timestamp)…
Status dots stay neutral until real state arrives — never a fake green. Shown
only past 200 ms; under that, nothing flashes."*

### 7.2 Empty — never a dead end, always the next verb

`EmptyState` with `title` + `hint` + one `action`. The ones that exist:

- **⌘K, no matches** — `Nothing matches "notify-svc"` / `Not a project, server,
  or page you can see.` / **+ New project**. The canvas's verb was
  `+ Create application "notify-svc"`, which the palette cannot honour honestly:
  an application is created *inside an environment* and the palette has no way
  to say which. Projects is where every creation in this product starts, so that
  is where the pill goes. (Recorded in `command-palette.tsx`.)
- **Templates, search miss** — `No template called "umami" yet` /
  `361 templates is the bar and the catalog is community-driven.` /
  **Request it ↗** · **Deploy from a repo instead**.
- **Previews, none yet** — glyph `⎇`, `No preview environments` /
  `Open a pull request and one appears here with its own URL — torn down when
  the PR closes.` / **Set up the PR webhook**. When the app deploys from an
  image the hint tells the truth instead: *"Previews follow pull requests on a
  git source — this app deploys from an image, so none will appear."*
- **Audit log, filter miss** — filter-aware, as the canvas wrote it: the title
  echoes the active filter and the two verbs are **Widen to 7 days** and
  **Clear filters**, rather than a generic "no matching entries".

**The golden path is chained empty states** (ui-principles §11), which is why
`EmptyState.emphasis` exists: an accent hairline with no fill, reserved for the
step that is the next thing to do on a fresh panel. `JoinServerFirstDialog` is
the same idea defensively — the create pill still opens something useful when
there is no server yet.

### 7.3 Error, offline, forbidden, throttled

All five live in `components/error-page.tsx`, all five take `embedded` so the
same design can render inside a region, and `QueryError` (§4) routes to them
from a real API answer.

```ts
NotFoundPage({ resource?; backTo = "/projects"; backLabel = "← Projects"; auditLogHref = "/settings/audit"; searchable = true; embedded? })
ForbiddenPage({ action: string; needs: Role; held?; scope?: "panel"|"team" = "team"; owners? = []; onRequestAccess?; embedded? })
ForbiddenForError({ error: ApiError; embedded? })
PlaneOfflinePage({ retryEverySeconds? = 5; retrying? = false; lastSyncLabel?; onRetry?; embedded? })
ServerFaultPage({ error?; onReload?; reloading? = false; embedded? })
ThrottledPage({ secondsLeft?; totalSeconds?; onTryAgain?; embedded? })
ErrorForRoute({ error: unknown })
useSecondsLeft(until: number | null | undefined): number | undefined
requiredRoleFor(method: string, path: string): { needs: Role; scope: "panel" | "team" }
actionFor(method: string, path: string): string
```

**404** — a 96 px mono numeral with the middle digit in the accent.
*"This page doesn't exist."* / *"Or it did — `notify-svc` may have been deleted
by a teammate. The audit log remembers."* Actions: **← Projects** · **⌘K
Search** (which calls `openCommandPalette()` rather than faking a keystroke) ·
**Audit log** now defaults to `/settings/audit` rather than being opt-in: the
prop was held back while the audit page did not exist, and it does now, so every
404 offers the third way out the reference draws. `searchable` exists for the one
surface that cannot: ⌘K is requested by a window event that `<CommandPalette />`
listens for from the `_app` layout, and the root route's unknown-URL 404 renders
outside it, so that surface turns the action off rather than drawing a pill that
does nothing.

A 404 whose way out is
another 404 is the dead end this page exists to avoid.

**403** — *"You can see this, but not touch it."* / *"Deploying to
**production** needs the **developer** role — you're a **viewer** on this
team."* Actions: **Request access from sam@** · **Back**, with
`owners: sam@meridian.dev` in mono underneath. `ForbiddenForError` derives the
action and the required rank from the failed request via `requiredRoleFor()` and
`actionFor()`, so the page names the *specific* fix rather than "insufficient
permissions".

**Plane offline** — the calmest page in the product, because the situation is
calm. A dashed `⇅` ring, then *"Can't reach the control plane."* /
***"Your apps are still serving.** Routing and containers live on the servers,
not here — the panel is only the steering wheel."* An amber dot with
`retrying in 4s · last sync 40s ago` in a `role="status" aria-live="polite"`
region, and **Retry now**. The countdown remembers a *deadline*, not a count, so
a re-render cannot stretch a second.

**500** — *"The panel hit a bug. Your fleet didn't."* / *"This request failed
inside cypherd. It's logged — attach the details if you file an issue."* Then a
mono ink chip and **Reload** · **Report issue ↗**. *Divergence:* the canvas
draws a `trace_8fk2-x91b-04aa` id; cypherd does not stamp one into the response
yet, so the chip carries what the response actually said — the request line and
the status — which is the same thing an issue needs, minus the lookup.

**429** — *"Too many attempts."* / *"Sign-in from this client is paused after
repeated failures. Try again in **0:47** — or use a recovery code if you've lost
your device."* A draining accent bar, and an `sr-only role="status"` that speaks
the remaining seconds. When cypherd sends no `Retry-After` the page says *"in a
moment"* rather than counting down a number it invented, and offers a **Try
again** pill instead of a bar.

### 7.4 Blocking work

`BlockingProgress` (§5.1) for work that genuinely cannot be navigated away
from — the database restore is the only current consumer. `noCancelReason` is
required, and `progress` is omitted when there is no honest measure. Everything
else follows ui-principles §3: never block navigation, state lives on the
server.

---

## 8. Accessibility

WCAG 2.1 AA is the floor (ui-principles §9); canvas 14g is the spec. What the
code actually guarantees:

- **Focus ring** — 2 px orange at 3 px offset, `:focus-visible` only, identical
  in dark. One base rule (§6.6). Rows flip to an inset offset. The only
  overrides are the ones that draw their own indicator.
- **Status names** — every dot carries the word, not just the colour.
  `StatusDot` is `role="img"` with an `aria-label` unless `decorative` is set; `StatusBadge` is
  `aria-live="polite"`. Shape encodes severity independently of hue (§6.8).
- **Live regions** — pipeline stage changes and toasts announce via
  `aria-live="polite"`. **Log tails do not** — they would never stop talking;
  the stage summary speaks instead. `ActionButton` carries its own `sr-only
  aria-live` span so a state change is heard, not only seen.
- **Motion** — `prefers-reduced-motion: reduce` kills `status-pulse` and
  collapses animations/transitions to 0.01 ms; `ActionButton`'s spinner becomes
  a static `▸`; `toastWorking` uses `motion-reduce:animate-none`.
- **Tab order** — top bar → page header actions → content rows, **each row one
  stop**, Enter opens, actions inside the row via ← →. Dialogs trap focus, Esc
  closes, focus returns to the opener (Radix, unmodified).
- **Contrast** — all text ≥ 4.5:1 in both themes; the dark status values were
  lifted for exactly this. Disabled states keep ≥ 3:1 **and name the reason in a
  tooltip, never contrast alone** — which is the whole argument for
  `disabledReason` over the native `disabled` (§5.1).
- **Names over nodes** — `Drawer` separates `title` (visible node, `aria-hidden`)
  from `label` (the accessible name); `DialogContent hideTitle` keeps the
  accessible name while painting nothing; `Field` computes `describedBy` so a
  field never points at a description that is not rendered.
- Decorative glyphs are `aria-hidden` and paired with an `sr-only` word; every
  skeleton is `aria-hidden` and its container `aria-busy`.

---

## 9. The keyboard model

`lib/keys.ts` owns the two rules every shortcut obeys, written once:

```ts
isTyping(target): boolean        // a key pressed into a field is text, not a command
overlayOpen(): boolean           // an open dialog/menu/alertdialog owns the keyboard
shouldIgnoreKey(e): boolean      // the two, plus modifiers and IME composition
```

`overlayOpen()` matters more than it looks: a `1` pressed behind a confirm must
not move the page the operator is confirming against.

**The vocabulary** — `SHORTCUTS` in `shortcuts-overlay.tsx` is the contract, and
`?` opens it:

| Key | Does |
|---|---|
| `⌘K` | jump to anything |
| `g p` / `g s` / `g i` | projects / servers / inbox (a chord, armed for 1200 ms) |
| `d` | deploy (on an app route) |
| `l` | logs (on an app route) |
| `j` / `k` | next / previous row |
| `1`–`7` | app tabs |

*"No shortcut triggers anything destructive — deletes and rollbacks always go
through their confirms."* That sentence is why the vocabulary is safe to learn
by pressing things, and it is a constraint on anything you add.

**How the pieces join.** The shell (`routes/_app.tsx`) binds one listener,
reading the current route through a ref so a navigation does not rebind and
reset a half-typed `g` chord. `APP_TABS` is the shared source for both the
`1`–`7` keys and the tab strip, so a tab added to the strip is addressable.
`d` does not reach three routes down: the shell fires `requestDeploy()` and the
deploy button subscribes with `useDeployShortcut(trigger)`, hitting the same
busy-guarded path the click uses.

**Rows opt in with one attribute.** `useRowNavigation()` returns a ref for the
list; each row carries a bare `data-row`, and optionally `data-row-open` to name
the element Enter should activate.

```tsx
const rows = useRowNavigation();
<ul ref={rows}>…<li data-row>…</li>…</ul>
```

It is a roving-tabindex composite: exactly one row is in the tab order, the rest
are reachable by key, and controls *inside* a row are reached by ← → rather than
Tab. `j`/`k` work from anywhere on the page — that is what the overlay promises
— while ↑ ↓ Home End only act once focus is inside the list, so the arrows keep
scrolling the page everywhere else. Clicking into a row makes it the current
stop, so the next `j` continues from where the pointer left off.

---

## 10. Mobile

**One breakpoint carries almost everything: `sm`.** The usage histogram is
`sm:` ×175, `lg:` ×8, `md:` ×7, `xl:` ×3. If you find yourself reaching for
`md:`, check whether `sm:` and a flex-wrap would do — the layouts are built to
have two states, not five.

**360 px is the floor** (ui-principles §9), and it is a real constraint that is
named in the source where it bites: `FactCard` stacks its `dt`/`dd` below `sm`
with the value on the left, because at 360 px a label and a value on one line
leaves nothing for the value; `SkeletonRows` avoids a grid that would push past
360 px, because a horizontal scrollbar on every loading state is worse than a
narrower skeleton; the app tab strip carries a `short` label per tab for exactly
this width.

**Navigation below `sm` is a fixed bottom bar** — Projects · Servers · Inbox ·
Profile, thumb-reachable, with the active item carrying the same accent rule as
the top bar pulled onto the bar's own hairline, and
`pb-[max(0.75rem,env(safe-area-inset-bottom))]` so a notched phone's home
indicator is not drawn through the labels. Templates and Settings stay reachable
through ⌘K (which becomes a real search *button* on a phone, since a phone has
no ⌘K) and the account menu — where a phone puts the things it does not visit
every hour.

*Note the divergence from ui-principles §4*, which says the four items "stay a
bar and scroll horizontally rather than becoming a second navigation model to
learn". The canvas (14b/14e) draws a bottom bar and the code follows the canvas.
The top bar stays 56 px at every width — the nav *moves* rather than wrapping
onto a second full-width line, which used to push ~90 px of chrome above every
page. This is a real, recorded disagreement between the two documents; treat it
as settled in the canvas's favour, and if you disagree, that is a
ui-principles PR, not a component change.

**Other mobile rules in force:**

- `Drawer` is a bottom sheet with a grab handle below `sm` (`animate-sheet-up`,
  the only upward shadow) and a right-hand column from `sm`
  (`animate-drawer-in`).
- Tables collapse to stacked cards via one grid string, e.g. servers'
  `flex flex-col gap-2 sm:grid sm:grid-cols-[2fr_1.2fr_1fr] sm:items-center sm:gap-4`.
- The toast card is 360 px but `max-w-[calc(100vw-2rem)]`, so it yields to the
  gutters.
- The theme toggle is hidden below `sm`: the phone's header carries only
  search and the bell, and the Profile tab's Light/Dark/Auto field is where a
  phone changes theme. Both write the same preference, so neither can disagree
  with the other.
- Wide content scrolls inside its own container; the page body never scrolls
  horizontally.

The test that matters is ui-principles §9's: *monitoring and deploy status must
work on a phone*, because P1's success moment happens there. The mobile deploy
sheet (pipeline stages plus a live log pane, canvas 14c) is that test made
concrete.

---

## 11. NOT BUILT — what the canvas specifies and the code does not have

Listed so nobody documents a wish as a fact, and so a later diff against the
canvas is a short list rather than an audit.

**Whole screens the canvas designs that have no route or component:**

- **Interactive terminal** (canvas 3d) — roadmap Phase 4 scope, WebSocket, and
  it carries its own security section (threat-model §5.6). No component exists;
  the word "terminal" in the source is always about *terminal states* or the
  ink pane idiom.
- **Metrics and observability charts** (canvas 3e, 9f) — no chart code, no
  `dataviz` consumers. Phase 4 scope, unstarted.
- **Coolify importer UI** (canvas 3j, 12e — "Importing from Coolify… 7 of 11",
  the background-safe progress card that lands its result in the inbox). The
  importer exists as backend/tooling; there is no UI for it.
- **Scale, autoscale, cloud burst** (canvas 11a–11d, 12a) — the canvas itself
  marks these **PROPOSED**. Not built, and not in the V1 cut.
- **SSO** (canvas 7g), **usage** (7e), **DR** (8l), **log drains** (6f) — no
  routes. Log drains are Phase 4 scope; the others are post-v1 per the feature
  matrix.
- **Visitor-facing 502 / 503 / 404 pages** (canvas 8f–8h) — these are proxy
  pages served to an app's visitors, not panel UI. Nothing in `web/` renders
  them.

**Designed details missing inside screens that do exist:**

- **The 500's trace id.** cypherd does not stamp a trace id into the error
  response body, so the chip carries the request line and status instead (§7.3).
  The canvas's `trace_8fk2-x91b-04aa` is a plane change, not a UI change.
- **The audit log's "Widen to 7 days" verb** is deliberately absent. The canvas
  pairs it with **Clear filters** on the filter-miss empty state, and the second
  one is built — but the list has no date filter to widen, so the first verb has
  nothing to act on. It arrives with a time range, not before.
- **The palette's create verb** is `+ New project`, not the canvas's
  `+ Create application "<query>"` — deliberately, and the reason is recorded in
  the component (§7.2).

**Components the plan named that were deliberately not built:** `ResourceTable`,
`RoleGate`, `EnvSwitcher` — see §5.3 for why each is absent and what replaced it.

**Vestigial in the token sheet:** `.rule-ink` (zero call sites, §6.5).

---

## 12. Adding a screen tomorrow

The short version of everything above, in the order you will need it.

1. **Declare the trail.** `useCrumbs([...])` in the route; `PageHeader` picks it
   up. Do not look for a `crumbs` prop.
2. **Wrap every data region in `PageState`.** That is the four states, the
   200 ms gate, and the designed error pages, for free. Give `SkeletonRows` a
   `columns` string that matches your real grid.
3. **Reach for the existing component.** Status is `StatusBadge` and nothing
   else; a status string is normalised through `normalizeStatus`. An action with
   a lifecycle is `ActionButton` with `state`, never a hand-rolled `busy` flag. A
   form control is `Field` with its render child. A destructive action is
   `ConfirmDestructive` with a real blast-radius sentence.
4. **Write tokens, not colours.** No hex literals, no `dark:` variants — if you
   need one, the token is missing.
5. **Name the reason on every disabled control.** `disabledReason`, always.
6. **Give rows `data-row`** and the list `useRowNavigation()`, so the keyboard
   vocabulary works on your screen too.
7. **Check it at 360 px** and in both themes, then read it cold: every visible
   word is plain language or carries its one-line `InlineHint`
   (ui-principles §11).
8. **Copy comes from the [glossary](../glossary.md)**, sentence case, outcomes
   not mechanisms. An error names its remedy; an empty state names its next
   verb; no screen is one you can only stare at.

The definition of done is still `web-ui-design.md` §6's: four states · both
themes · 360 px usable · keyboard operable · glossary copy · data only via the
generated client · role-gated actions render their rank · the "explain it cold"
pass · a Playwright smoke.

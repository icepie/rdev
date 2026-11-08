# Context sheet and table alignment

Reference: BB checkout `/tmp/rdev-bb-reference`, baseline `f51e6b0`.

Compared `ThreadContextWindowIndicator.tsx` and `markdown-preview.tsx` with the rdev components. The context card was retaining its desktop fixed width, border and shadow inside the mobile drawer. Applied BB's mobile full-width card overrides, padding, larger touch typography and progress height; provided the localized drawer title. Token counts remain omitted per product preference.

Markdown tables now preserve intrinsic content width and scroll inside a constrained wrapper. This deliberately avoids narrow CJK columns; unlike BB's full transcript breakout implementation, this change remains contained within the message.

Validation:

- 75 Web test files / 296 tests passed, including mobile drawer naming and compaction busy-state behavior.
- TypeScript and production build passed.
- Chromium component fixture, actual application CSS, 390 × 844 touch viewport: drawer width 390px, card width 388px, card border 0px, transparent shadow, progress width 356px, padding 8px 16px 16px, trigger width 44px.
- CJK table fixture: table 581px, scroll container 358px, first body cell 73 × 29px; document width remained 390px.
- Geometry was measured through the DOM; no screenshot-based visual approval or complete BB parity is claimed.

Further source audit remains: composer content growth, integrated Files/Terminal/Desktop toolbar triggers, background job density, tooltip behavior, and table breakout behavior. These are not completed by this focused patch.

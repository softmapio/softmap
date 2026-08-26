# softmap backlog

Deferred items from the pre-demo UX review — recorded, not yet implemented.

- **Transaction frames.** Group effects that share one `sql.Tx` (Begin →
  Commit/Rollback) into a visual frame on the map, so an analyst sees which
  writes are atomic together. Needs dataflow from `BeginTx` results to the
  driver calls made through them.
- **Auth/middleware surfacing.** Show where identity comes from: the
  middleware chain in front of the handler (JWT parse, session lookup) and
  which claim/field feeds `userID`-style parameters into the flow.
- **Scan provenance stamp.** Commit hash + scan date rendered on the map
  header (and in the JSON), so a shared HTML is traceable to the code state
  it describes.
- **Clickable source links.** `file:line` on cards/panel opens the editor or
  repo browser (configurable URL template, e.g. GitLab `-/blob/<ref>/`).
- **Error→HTTP mapping edges.** When a service error maps to a status via a
  getErrorCode-style switch, draw the mapping (sentinel → 4xx) as an edge or
  annotation instead of leaving the handler-level 500 as the only outcome.
- **Layer swimlanes.** Optional handler / usecase / repository columns so the
  outline also communicates architecture depth.
- **Terminal placement.** Success terminal pinned to the right edge of the
  canvas, mirroring where flowchart readers expect the end state.
- **Readability lint (rename hints).** softmap already knows every place a
  cryptic 1–2 letter variable (`s`, `o`, `sc`) feeds a rendered decision —
  the viewer refuses to show such names and falls back to provenance. Emit
  the same list as a report for developers: `file:line — rename <s> so the
  map can say "no shop" instead of hiding the name` (e.g. a
  `scan --lint-readability` flag or a section in index.json). Same idea for
  conditions that fail business templating (OR-trees, cryptic idents): each
  is a concrete, code-level fix that makes the Business map read better.

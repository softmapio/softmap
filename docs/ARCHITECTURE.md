# softmap — how it works under the hood

softmap statically extracts **flow maps** from Go codebases: given an
entrypoint (an HTTP handler, a Kafka consumer), it walks the downstream call
chain, throws away everything that is not a meaningful step, tags the calls
that cross a boundary (SQL, Redis, HTTP, Kafka), and emits one JSON document
per flow. The code under analysis is **never executed**.

This document explains the pipeline stage by stage, then covers the three
extension scenarios: adding support for another language, adding new
integrations (frameworks, effect libraries, workers/queues), and changing the
filtering logic.

---

## 1. The pipeline

```
                 ┌────────────────────────────────────────────────────────┐
   Go module ──▶ │ loader   load once, build SSA, classify files          │
                 └───────────────┬────────────────────────────────────────┘
                                 ▼
                 ┌────────────────────────────────────────────────────────┐
                 │ entrypoints   scan call sites for router/consumer      │
                 │               registrations → [Entrypoint]             │
                 └───────────────┬────────────────────────────────────────┘
                                 ▼
                 ┌────────────────────────────────────────────────────────┐
                 │ graph.Build   whole-program call graph, built ONCE     │
                 │               (VTA, falling back to CHA)               │
                 └───────────────┬────────────────────────────────────────┘
                     per entrypoint ▼
                 ┌────────────────────────────────────────────────────────┐
                 │ graph.Extract   BFS from the entrypoint → Flow IR      │
                 │                 (effects detected inline, BFS stops    │
                 │                 at them; async/dynamic/multi tagged)   │
                 └───────────────┬────────────────────────────────────────┘
                                 ▼
                 ┌────────────────────────────────────────────────────────┐
                 │ filter.Mark + Prune   rule pipeline over the Flow      │
                 │                       (the product lives here)         │
                 └───────────────┬────────────────────────────────────────┘
                                 ▼
                 ┌────────────────────────────────────────────────────────┐
                 │ output   deterministic JSON (schema v1), --debug-tree  │
                 └────────────────────────────────────────────────────────┘
```

Two principles shape everything:

- **Load once.** The whole program is loaded and analyzed in one pass via
  `golang.org/x/tools`. There is no per-symbol querying of any external
  process (the mistake that makes LSP-based analyzers take 20+ minutes).
- **Readability beats completeness.** Every stage is biased toward dropping
  noise as early as possible, and every drop is recorded and explainable
  (`--debug-tree` shows the rule id next to everything it removed).

### 1.1 loader (`internal/loader`)

One `packages.Load` call with syntax + type information, then one SSA build
(`ssautil.Packages` + `prog.Build()`, with `ssa.InstantiateGenerics`).
Everything downstream works off the resulting `loader.Program`.

Three things happen here that matter later:

- **Test files never load** (`Tests: false`), so they cannot inflate any
  later phase.
- **Generated/vendored files are classified immediately post-load** — by
  filename pattern (`*_gen.go`, `*.pb.go`), by the stdlib `ast.IsGenerated`
  check for `// Code generated ... DO NOT EDIT.` headers, and by `/vendor/`
  path segments. They **cannot be dropped at load time**: a `.pb.go` file
  often defines types the handwritten code needs to type-check. Instead,
  `FuncClass(fn)` lets every later phase exclude them: they never appear in
  entrypoint discovery or in output (extraction pre-marks them with the
  pseudo-rule ids `excluded:generated-file` / `excluded:vendor`).
- **Dependencies load types only, not bodies.** Deps come from export data,
  so their functions exist as declarations without SSA bodies. This is a
  feature: traversal stops at the module boundary automatically, and the
  boundary calls that matter are captured as effect nodes instead.

The `GeneratedStats` count feeds the honesty warning ("82% of module
functions are in generated files") printed by the CLI.

### 1.2 entrypoints (`internal/entrypoints`)

Discovery scans SSA **call instructions** (not the AST) of every
module-local, non-generated function, looking for registration patterns.
SSA is the right level because the loader already resolved every identifier:
`r.POST(...)` is unambiguously `(*gin.RouterGroup).POST` no matter how it
was imported or wrapped, and handler arguments (closures, bound methods
like `h.CreateOrder`, `http.HandlerFunc(f)` conversions) are plain SSA
values that `internal/ssax` can chase to the declared function.

Each framework is one `matcher` function registered in the `matchers` table
(`matchers.go`): net/http (including Go 1.22 `"POST /path"` patterns), gin,
echo, chi, gorilla/mux, fiber, sarama (`ConsumerGroup.Consume` → the
handler's `ConsumeClaim`), segmentio/kafka-go and confluent (a function
looping over `Reader.ReadMessage` / `Poll` *is* the entrypoint). The
`Frameworks` constant next to the table is what user-facing messages print,
so the CLI can never advertise a framework the table does not cover.

Routers group routes in two shapes, and both compose prefixes: chi and
fiber's `Route` pass a subrouter into a **callback**, walked outward by
`callbackRegistrar`; fiber's `Group` returns a subrouter **value**, chased
backwards through its receiver by `fiberPrefix` (including across the Router
interface, where the receiver is the interface value rather than an
argument). Majors of one library that differ in argument shape are one
matcher, keyed on the version in the import path — fiber v2 takes a variadic
handler list whose last element is the endpoint, v3 names the endpoint first
and takes middleware after it.

Route paths and topics resolve through the same constant-chasing machinery
as everything else (see 1.5). Discovery is **best effort by design** — the
`--entrypoint func:<pkg>.<Name>` escape hatch (`Resolve`) accepts any
function, with suffix matching and an "ambiguous, candidates are:" error.

Entrypoint IDs are stable and human-readable: `http:POST:/orders`,
`kafka:orders.created:consumer.Run`, `func:...` as fallback; collisions get
`#2` suffixes in source order. Route parameters are normalized to one
brace-delimited form by `normalizeRoutePath`, so an endpoint gets the same id
whichever router registered it (`:id` and `*` become `{id}` / `{*}`; chi,
gorilla and net/http already write braces). Only the matchers of colon-syntax
routers — gin, echo, fiber — may call it: to the brace-syntax routers a
segment starting with `:` or `*` is an ordinary literal, and rewriting it
there would rename a real route and could collide it with that router's own
`{id}` route. `Resolve` normalizes what the user typed too, and tries the raw
spelling as well, so an id written either way resolves.

### 1.3 graph.Build (`internal/graph/build.go`)

The call graph is built **once** for the whole program, then every flow is
extracted from it.

Algorithm choice (kept behind the `Builder` interface so it is swappable):

- **CHA** (class hierarchy analysis) is nearly free but over-approximates:
  an interface call gets an edge to *every* type implementing the method.
- **VTA** (variable type analysis) propagates concrete types through a
  whole-program flow graph and resolves interface calls precisely — at a
  cost that grows steeply with program size.

`--algo auto` (default) does both, cheaply: build CHA first, compute the
CHA-reachable cone from the entrypoints **plus the module's `main`/`init`
functions**, and run VTA restricted to that cone with the CHA graph as its
initial approximation. The main/init addition is essential and easy to get
wrong: VTA can only resolve `s.notifier.Notify()` if it *sees the allocation
site* where the concrete notifier was stored into that field — and that
happens in constructors called from `main`, not in code reachable from the
handler. Fallbacks to CHA: cone larger than `MaxVTAFuncs` (20k) or VTA
exceeding `--vta-timeout` (120s). Every fallback prints a warning that
interface edges may over-approximate — degradation is never silent.

Even a successful VTA run keeps the CHA graph around (`Result.CHA`) for a
**per-call-site fallback** during extraction: when VTA resolves an
interface call to *nothing*, which is the signature of reflection-based
dependency injection (fx, wire, dig — the allocation happens inside the
container, invisible to type propagation), extraction consults CHA for that
one site. A unique implementation counts as resolved (`static`), up to 5
become `static-multi` edges, more stays an honest `dynamic` terminal.
Without this, a typical handler → UseCase-interface → repository codebase
loses its entire flow at depth 1.

### 1.4 graph.Extract — the Flow IR (`internal/graph/extract.go`)

`Extract` BFS-walks the call graph from the entrypoint and produces the
**Flow IR** — the single mutable data structure shared by the filter, the
debug tree, and the JSON writer:

```go
type Node struct {
    Name, Pkg, Pos   string        // identity + location
    Fn               *ssa.Function // nil for synthesized terminals
    Kind             string        // step | effect | terminal
    Effect           *effects.Effect
    Async            bool          // reached via `go f()`
    Resolution       string        // static | static-multi | dynamic | truncated
    Out, In          []*Node       // adjacency (DAG + back-edges for recursion)
    Collapsed        []string      // wrapper names inlined into this node
    DroppedBy        string        // rule id that removed it ("" = kept)
    Kept             bool          // immune to rules (effects, terminals)
}
```

Decisions made during extraction:

- **Effect detection runs inline and stops the walk.** When a call site
  matches an effect detector, the module method making the call absorbs it
  into its `effects` list (driver calls are never nodes) and BFS does not
  descend into library internals. This alone kills most raw-graph spaghetti
  before the filter even runs. When the budget pass collapses an
  effect-carrying step, its effects bubble into the surviving ancestor,
  deduplicated — what a subtree ultimately does stays visible.
- **Interface calls:** one resolved callee → `static`; several →
  an edge to each implementation, all tagged `static-multi` (downstream
  layers can render the uncertainty); zero → a synthesized `terminal` node
  tagged `dynamic` — *never silently dropped*. Dynamic terminals are only
  created for module-local interfaces and function values; an unresolved
  `rows.Scan` on a dependency interface is boundary plumbing, not
  meaningful uncertainty.
- **`go f()`** tags the target node `async` (OR over incoming edges);
  collapse later propagates the tag so `go wrapper()` → effect keeps it.
- Builtins (`len`, `append`, …) and `err.Error()` are not calls in a flow
  map and are skipped.
- Synthetic wrappers (promoted/bound methods) and generic instantiations
  are canonicalized to their declared functions, so `Map[int]`/`Map[string]`
  is one node.
- `Limits{MaxDepth: 40, MaxNodes: 5000}` guard runaway graphs; hitting one
  marks frontier nodes `terminal/truncated` and warns.

### 1.5 Constant resolution (`internal/ssax`)

Shared machinery used by entrypoints (route paths, consumer topics) and
effects (SQL text, URLs, producer topics). `ConstString` chases an SSA value
through, in order: constants, conversions, loads of single-store locals,
package-level vars stored once in `init`, and — one interprocedural level —
struct fields with exactly **one store anywhere in the program** (found via
a memoized program-wide field-store index). That last step is what resolves
the dominant real-world pattern:

```go
const OrderCreatedTopic = "orders.created"
func New(...) *Producer { return &Producer{w: &kafka.Writer{Topic: OrderCreatedTopic}} }
func (p *Producer) OrderCreated(...) error { return p.w.WriteMessages(...) } // topic resolves
```

Anything beyond that (env vars, computed strings) degrades **honestly**:
`"topic": null, "topic_expr": "<hint>"`. The chase is depth-limited (5) —
this is deliberately not a general constant propagator.

### 1.6 effects (`internal/effects`)

Table-driven detectors match the callee's `(package path, receiver or
interface type, method name)` — identical for static and interface-dispatch
calls, with `/vN` major-version suffixes normalized. Families: database/sql,
pgx (+pgxpool), sqlx, gorm, xorm; go-redis v8/v9 (package + client receiver
+ any exported method — more robust than allowlisting ~200 commands);
net/http client calls; kafka producers (segmentio, sarama sync/async,
confluent). Effect nodes are born `Kept: true` — rules can never remove
them. Effects attach to the absorbing module method in call-site order;
identical entries (same type+detail+topic) deduplicate per node, so 64
`xorm.Session.Find` calls bubbling into one ancestor read as one line.

### 1.7 filter (`internal/filter`) — the product

The defaults ship as an **embedded YAML file**
(`internal/filter/rules/default.yaml`, printed by `softmap rules
--defaults`) in exactly the format a future `--rules` user file will use.
A rule is match criteria (package globs with `std:`/`dep:` pseudo-prefixes,
function-name globs, or a named heuristic) plus an action (`drop` or
`collapse`). Rules apply in file order; first match wins.

Heuristics are **named, individually unit-tested predicates**
(`predicates.go`): `logger-method`, `logger-package`, `metrics-tracing`,
`config-reader`, `validation-helper`, `trivial-wrapper`, `getter`,
`error-wrapper`. A rule file referencing an unknown name fails at load with
the list of valid names.

The pass order in `Mark`/`Prune` is what makes aggressive rules safe:

1. **Effect immunity** (engine-level, not expressible in YAML): effect
   nodes and dynamic terminals are never touched by rules. This is why a
   blanket "drop all stdlib" rule cannot lose a `database/sql` query.
2. **Rule pass** marks nodes dropped/collapsed — nothing is removed yet, so
   `--debug-tree` can show every decision with its rule id.
3. **Effect-free-subtree pass** (`engine:no-effect-subtree`): unmarked
   nodes deeper than the narrative depth (2) whose subtree contains no
   effect are implementation detail, not flow. Only *effects* anchor a
   subtree — dynamic terminals are shown when their context survives but
   never rescue ancestors (lesson from Gitea: its whole logging subsystem
   was resurrected by one unresolved writer interface).
4. **Keep-protection** (bottom-up, cycle-safe): any drop-marked node with a
   surviving descendant is un-dropped. *Paths to effects never break.* One
   pass suffices because "has a base-surviving descendant" is transitive.
5. **Fit-to-budget** (`engine:beyond-narrative`): if survivors still exceed
   the ~40-node readability budget, the flow zooms out — find the largest
   depth D whose steps (plus all effect nodes) fit, and mark every deeper
   step as collapse. Deep effects splice up to their nearest surviving
   ancestor and deduplicate, so a giant subtree summarizes to "this step
   ultimately does: Insert, Update, Find…". This is the single biggest
   lever on real codebases (Gitea create-pull-request: 1638 raw → 25 kept).
6. **Prune + splice**: collapse-marked nodes are inlined — children
   re-parented to the nearest surviving caller, which records the wrapper
   names in `collapsed` (chains accumulate: `A→w1→w2→B` becomes `A→B` with
   `A.collapsed=[w1,w2]`; beyond-narrative collapses skip name recording,
   and the JSON dedupes and caps the list at 24); drop-marked nodes are
   removed with their subtrees; per-rule removal counts become
   `stats.dropped_by_rule`.

`--no-filter` skips this stage entirely (effect *detection* still runs — it
is analysis, not filtering).

### 1.8 Guards & outcomes (`internal/guards`) — the "why" layer

After filtering, each surviving step's SSA is scanned for **guards**: `If`
branches where exactly one successor reaches, through a straight-line chain
(≤5 blocks, tolerating logging and defer's RunDefers), a `Return` carrying a
non-nil error. Chosen over postdominator analysis because it is small,
deterministic, and fails safe (a miss, not noise). `&&`/`||` conditions
lower to several SSA branches — decisions dedupe by the enclosing source
`*ast.IfStmt`, whose condition is printed verbatim (including non-English
text). A tail `return doWork()` whose error simply *originates* inside the
branch is continuation, not a rejection, and is skipped.

The classifier (`mechanicalPropagation`) separates two worlds:

- **Mechanical propagation** — condition is `err != nil`/`== nil` on the
  error of a call in the same function, and the branch returns that error
  or a wrap of it (`%w`/`%v`/`%s` fmt.Errorf, pkg/errors.Wrap*,
  errors.Join). Never a node; the failing call's node gets `fallible: true`.
  When in doubt, classify mechanical.
- **Semantic guards** — everything else that ends the flow with an error:
  business conditions, permission gates, checks translating a failure into
  a distinct sentinel. These become `decision` nodes (condition text +
  `uses` provenance: the same-function calls feeding the condition) chained
  under their function, each with a `fail` edge to an `exit` node carrying
  the error identity (sentinel name / verbatim message / unknown). Calls
  whose site is dominated by a guard's pass branch move onto that decision,
  so the happy path reads as a spine with rejections branching off.

- **Gates** — an `if` where *neither* branch exits but exactly one side
  holds visible work (kept calls or absorbed effects): feature flags, cache
  short-circuits, "whitelisted clients skip the debt check". They render as
  `decision` nodes with `gate: true`, no exit — the gated calls nest under
  the condition and absorbed driver effects made inside the gated region
  move onto the card. Structural requirements keep them quiet: both sides
  must rejoin the flow (loop headers and early returns never qualify),
  `err != nil` shapes never gate, and a gate whose work later collapses away
  is removed rather than left dangling.

Budgets keep the Miro silhouette readable: ≤8 decisions per function
(priority: sentinel > message > gate; overflow → `checks_overflow`), soft
~80 total rendered elements filled best-outcome-first (a gate costs one
card, a guard two), shallow functions first. Error exits claimed by decisions (or belonging to mechanical wraps)
leave `error_exits`, so nothing displays twice. To recognize
project-specific validation helpers as guards, extend the classifier — it
is a named, table-tested predicate like the filter heuristics.

### 1.9 output (`internal/output`)

`FromFlow` converts the (filtered or raw) Flow into the schema-v1 document.
Everything is deterministic — node ids (`n1…`) follow BFS order over edges
that were built in deterministic order, `collapsed` lists are sorted, and
`encoding/json` sorts the stats map — so golden tests compare bytes.
`WriteDebugTree` renders the marked-but-unpruned flow as an indented tree
with `[dropped: rule-id]` annotations: the fastest way to eyeball whether a
flow reads well and which rule to blame when it does not.

### 1.9a entities (`internal/entities`) — the noun-first index

`Build` derives business entities from the emitted flow documents — it runs
over the output, not the SSA, so it needs no extra analysis pass. Signals
per flow, strongest first: SQL table names parsed from effect details
(verb → access kind: creates/reads/updates/deletes), Kafka topics
(`publishes`/`consumes <topic>`), then the first noun-like URL path segment
(mount prefixes `api`/`vN` are transparent; infrastructure routes like
`auth`, `healthz` give no signal). Keys are normalized to singular
(`products` → product) and satellite tables fold into an existing base by
`_`-prefix (`order_items` → order — only when `order` exists; a lone
satellite keeps its shelf). Flows with no signal land in `other`: an entity
is never guessed. `.softmap.yaml` `entities:` renames shelves (marked
overridden), `merge:` clusters manually. `scan --all` writes the result as
`index.json` (flow list + entities, same schema version) and embeds it into
the HTML viewer, whose Business mode opens on the entity shelf.

### 1.9 Testing strategy

- `testdata/toyshop` is a fixture service exercising every feature; its
  dependencies are **local stub modules** (`testdata/stubs/*`) with the same
  import paths and signatures as the real libraries, wired via `replace` —
  `go test` is hermetic and loads in ~1s. If you change what a matcher keys
  on, update the corresponding stub.
- A framework with registration shapes of its own gets its **own fixture
  module** rather than more routes in toyshop: `testdata/fibershop` covers
  fiber (both majors, nested groups, `Route` callbacks, wildcards, and
  middleware that must not be mistaken for an endpoint). Keeping it separate
  means adding a framework never churns toyshop's goldens, and the
  assertions read as a spec for that one router.
- Golden files (`testdata/golden/*`) pin the emitted JSON byte-for-byte
  (refresh deliberately: `go test ./internal/output -update`), and
  `assertFlowContent` pins the success criteria (noise gone, both interface
  impls tagged `static-multi`, effects + topic present, generated file never
  surfaces) independently of the golden bytes.
- Every predicate, effect detector, and matcher has table-driven tests.
- `scripts/bench-gitea.sh` is the real-world benchmark (clones Gitea,
  asserts the create-pull-request flow keeps 15–40 nodes incl. SQL).

---

## 2. Extending softmap

### 2.1 Adding a new integration (framework, effect library, worker/queue)

This is the most common change, and it is deliberately cheap. Ask: is the
thing an **entrypoint source** (work arrives), an **effect** (work leaves),
or both?

**New HTTP framework / worker consumer → one matcher.** Write a
`matcher` in `internal/entrypoints/matchers.go` and add it to the
`matchers` table. Pattern to copy: check the callee info (package path,
receiver type, method name), pull the route/topic/queue from arguments via
`ssax.ConstString` / `ssax.SliceStrings`, resolve the handler argument with
`handlerFunc` (or `ssax.ConcreteMethod` for interface-shaped handlers), and
return an `Entrypoint`. For poll-loop style consumers (kafka-go, most AMQP
clients) the *enclosing function* is the entrypoint — see
`matchKafkaGoConsumer`. If the router groups routes, compose the prefix with
`callbackRegistrar` (subrouter passed into a callback) or `fiberPrefix`
(subrouter returned as a value). Add a stub module under `testdata/stubs/`
with just the signatures you match on, give the framework its own fixture
module if its registration shapes differ from toyshop's, and add a discovery
test case. Add the framework to the `Frameworks` constant so the CLI's
zero-discovery hint stays truthful.

**New effect library (a message queue, a mail API, gRPC clients, an
external HTTP API SDK) → one detector.** Add a `detector` to the table in
`internal/effects/effects.go`: a `matches` func over `(pkg, type, method)`
and a `build` func producing `Effect{Type, Detail, ...}`. If the effect has
an "address" worth extracting (topic, queue name, URL, RPC method), resolve
it like `internal/effects/kafka.go` does and degrade to a `*_expr` hint
when dynamic. Currently `Effect.Type` is one of `sql|redis|http|kafka`; a
new boundary type (e.g. `grpc`, `queue`) is just a new string — the schema
carries it as-is, but bump consumers of the JSON accordingly.

**A new worker system that is both** (e.g. RabbitMQ, NATS, Temporal):
producer side is an effect detector, consumer side is an entrypoint
matcher. They share nothing except the package path constants, so implement
them as the two independent pieces above.

### 2.2 Changing the filtering logic

- **New heuristic:** add a predicate function in
  `internal/filter/predicates.go`, register it in the `predicates` map, add
  table-test rows in `predicates_test.go` (positive *and* lookalike-negative
  cases — see how `EmailNotifier.Notify` guards the logger heuristic), then
  reference it from `rules/default.yaml`. Rule order matters: put specific
  rules before general ones (`error-wrapper` precedes `trivial-wrapper` so
  the stats attribute drops to the most specific cause).
- **Tuning defaults:** iterate with `--debug-tree` (every decision is
  annotated) and `stats.dropped_by_rule` (tells you which rules earn their
  keep). The golden tests will show you the exact diff of any change.
- **Semantics changes** (new action, ordering, protection): all in
  `internal/filter/apply.go`. The invariants to preserve, in priority
  order: effects are immune; paths to kept nodes never break; collapse
  preserves connectivity; everything removed records which rule did it.
- The rule format is the public contract (`rules --defaults`). Additive
  changes (new heuristic names, new match keys) are safe; changing existing
  key semantics breaks user rule files once `--rules` lands.

### 2.3 Adding another language

The honest answer: the **backend half** of softmap is language-agnostic,
the **frontend half** is deeply Go-specific, and the boundary between them
is the Flow IR (`graph.Flow` / `graph.Node`) plus the entrypoint/effect
concepts. A second language means a new frontend producing the same IR:

Language-agnostic (keep as-is):
- The Flow IR and its semantics (steps/effects/terminals, async,
  resolution tags).
- The entire filter pipeline: rule format, heuristics *concepts*
  (logger/config/wrapper detection), keep-protection, collapse splicing.
  Predicates that inspect SSA bodies (`trivial-wrapper`, `getter`) would
  need per-language re-implementations behind the same names.
- The JSON schema, stats, debug tree, CLI shape, golden-test approach.

Go-specific (needs a per-language equivalent):
- `internal/loader`: needs the language's equivalent of "load the whole
  program with types, once" (TypeScript: the TS compiler API;
  Java/Kotlin: e.g. Spoon/JavaParser + a class-hierarchy pass; Python:
  realistically only heuristic resolution).
- `internal/graph.Build`: call-graph construction with *some* answer for
  dynamic dispatch. This is the hard part in any language; the
  static/static-multi/dynamic honesty model was designed so weaker
  resolvers can still emit truthful output — a Python frontend can tag
  nearly everything `dynamic` or `static-multi` and the rest of the
  pipeline still works.
- `internal/entrypoints` + `internal/effects` tables for that ecosystem
  (Express/Fastify routes, SQLAlchemy/Prisma, etc.).
- `internal/ssax`: per-language constant chasing.

Practical refactor when the time comes: define a `Frontend` interface
(`Load`, `Discover`, `BuildGraph`, `Extract` returning `*graph.Flow`),
move the Go implementation behind it, and dispatch on module type. Do not
attempt a shared IR *below* the Flow level (a cross-language SSA) — that is
a research project, not a feature.

### 2.4 Integrating with an API / running as a service

The CLI is a thin shell: `cmd/softmap/scan.go` just sequences
`loader.Load → entrypoints.Discover → graph.Build → graph.Extract →
filter.Mark/Prune → output.FromFlow`. Everything it uses is in `internal/`,
so today the integration path is either (a) exec the binary and parse the
JSON (stable, versioned via `schema_version` — this is the intended
contract for CI and other tools), or (b) promote the internal packages to a
public `pkg/` API once a second consumer exists. For a long-running service
(e.g. scan-on-webhook), note that a `loader.Program`+call graph is
per-module-version and cheap to rebuild relative to clone times; there is
no global mutable state besides the memoized field-store index (keyed per
program, GC'd with it), so concurrent scans of different modules are safe
in one process.

---

## 3. Honesty & degradation ledger

Things softmap deliberately does *not* claim to do, and how each shows up
in output instead of being hidden:

| Situation | What you see |
|---|---|
| Interface call, several implementations | edges to each, `"resolution": "static-multi"` |
| DI-wired interface (fx/wire), unique impl | resolved via per-site CHA fallback, `"static"` |
| Interface/function value nobody assigns | terminal node, `"resolution": "dynamic"` |
| Topic/route/query not a resolvable constant | `"topic": null` + `topic_expr` hint / method name as detail |
| VTA too slow or cone too big | CHA + stderr warning "interface edges may over-approximate" |
| Graph beyond depth/size limits | `"resolution": "truncated"` + warning |
| Mostly-generated module | stderr warning with the percentage |
| Zero entrypoints discovered | stderr warning pointing at `--entrypoint` |
| Every dropped/collapsed node | rule id in `--debug-tree` and `stats.dropped_by_rule` |
| Mechanical `if err != nil` propagation | `fallible` badge on the failing call, never a decision node |
| Semantic guards beyond the render budget | `checks_overflow` count on the step |

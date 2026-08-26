---
name: Bug report
about: A crash, a missed entrypoint, a wrong or missing effect, a flow that doesn't match the code
labels: bug
---

**What happened**

<!-- One or two sentences. "softmap said X, the code does Y" is perfect. -->

**Command**

```sh
# the exact command you ran, e.g.
# softmap scan . --all --html -o out
```

**Stats lines**

<!-- softmap prints phase=... lines to stderr, e.g.
     phase=load elapsed=2.7s module=... pkgs=362 funcs=78270
     phase=discover elapsed=95ms entrypoints=0
     Paste them all — they tell us where things went sideways. -->

```
```

**Debug tree (if the map content is wrong)**

<!-- Re-run with --debug-tree and paste the part of the tree around the
     wrong/missing node. It annotates every filter decision, so it usually
     answers "why was this dropped/kept" directly. -->

```
```

**Environment**

- `go version`:
- softmap version: <!-- output of: go version -m $(which softmap) | head -3 -->
- Target repo: <!-- public link if possible; otherwise framework/router used
     (chi/gin/echo/grpc/...) and rough size. Never paste private code you
     can't share — a minimal reproduction snippet is ideal. -->

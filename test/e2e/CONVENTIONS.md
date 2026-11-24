# E2E Test Conventions

This document defines the naming, labelling and output conventions for the e2e
suites in `test/e2e`. The goal is a **uniform test report**: every spec name in
the published JUnit output follows the same grammar, and every failure carries
the same kind of context.

CI runs the specs with `--junit-report`, uploads the XML and publishes it with
`dorny/test-reporter`. A JUnit `testcase` name is the concatenation of the whole
container chain plus the leaf text, and everything written to `GinkgoWriter`
(including `By` steps) lands in that testcase's `system-out`. Naming and
printouts are therefore both report-visible — they are not just local style.

## 1. Spec tree shape

Every suite uses the same three levels:

```
<Suite Identity>  >  <Phase>  >  [<subject>] should <assertion>
```

A fixed arity keeps JUnit names comparable across suites. Do not add or drop a
level; if a phase needs sub-grouping, fold the distinction into the leaf text.

### Level 1 — suite identity

Title Case, no `E2E` and no `Test Suite(s)` suffix (the Ginkgo suite is already
named `e2e suite`). The name matches the deployable topology the specs need, as
exposed by `make deploy-<name>`:

```go
var _ = Describe("Shared App Network", Label(labelSuiteSharedAppnetwork, labelIPv4), Ordered, func() {
```

Where one file drives several topologies from a table, the table entry's name is
the level-1 name — do not wrap it in an extra outer container.

### Level 2 — phase

Title Case, drawn from a closed vocabulary:

| Phase        | Contents                                                        |
| ------------ | --------------------------------------------------------------- |
| `Deployment` | control-plane convergence: conditions, resource counts, pod readiness |
| `Traffic`    | data-path verification: ICMP, TCP, UDP, SCTP                    |
| `Disruption` | deliberate fault injection                                      |
| `Recovery`   | state after a disruption has been undone                        |
| `Negative`   | assertions that something is correctly rejected or withdrawn     |
| `Comparison` | assertions that relate two previously measured outcomes          |

Do not append the suite name to a phase (`Deployment`, not `Deployment for MD5`)
— level 1 already carries it.

### Level 3 — leaf

Third person `should …`, lowercase after the optional subject bracket:

```go
It("should be Accepted", ...)
It("should distribute TCP traffic across targets", ...)
```

When a spec is generated per gateway, VIP or IP family, the varying subject goes
in a **leading bracket** so the report groups and sorts predictably:

```go
It(fmt.Sprintf("[%s] should be Accepted", gw.name), ...)
```

Not mid-sentence (`should have gw-a1 Accepted`) and not trailing
(`BGP is Up gw-t1`). Where the parameterization is a clean matrix, prefer
`DescribeTable`/`Entry` and let Ginkgo format it.

Avoid: `Gateway should be Accepted` (subject duplicated from the container),
`baseline: all targets receive traffic` (prefix instead of grammar),
`does NOT create ENC` (shouting), `VIP is reachable via ICMP` (declarative).

## 2. Labels

Labels — not spec text — select what runs. Every top-level tree carries exactly
one `suite:<name>` label plus the IP families it exercises:

```go
Label(labelSuiteBFDDetection, labelIPv4)
```

Both come from the registry in `e2e_conventions_test.go`; never write the string
literal inline. `suite:<name>` matches the `make deploy-<name>` /
`make test-<name>` target so the topology a spec needs is unambiguous.

Selection is then uniform between local runs and CI:

```bash
make test-bfd-detection          # --label-filter='suite:bfd-detection'
make test-ipv4                   # --label-filter='ipv4'  (all IPv4 suites, parallel)
```

This replaces `--focus="<regex>"` on spec text. Focus matches a regex against
the concatenated spec name, so the Makefile's suite selection silently depended
on spec wording: renaming a `Describe` left the target matching nothing, which
Ginkgo reports as a successful run of zero specs. `--fail-on-empty` (set in
`GINKGO_OPTS`) now turns that into a failure, and labels remove the dependency
on wording altogether. Label filters also compose, which focus cannot:
`--label-filter='ipv4 && !suite:md5'`.

Ad-hoc selection by text is still available for local reproduction, at any
depth of the tree, since `GINKGO_FLAGS` is appended last:

```bash
make test-md5 GINKGO_FLAGS='--focus="key rotation"'
```

A `ReportAfterSuite` check in `e2e_conventions_test.go` fails the run if any spec
lacks a `suite:` label or carries more than one, so the contract cannot rot.

Suites that must never run on a CI runner (currently `openshift-crc`) carry a
suite label but no IP-family label.

Keep `Serial` on every tree that drives a shared topology or the shared VPN
gateway, so it is never co-scheduled under `ginkgo -p`.

## 3. Printouts

### `By` steps

Mandatory for any spec with more than one observable step — they are the only
breadcrumbs a reader gets from `system-out` when a spec fails in CI.

Lowercase gerund phrase describing an **action**, no trailing colon:

```go
By("scaling the target Deployment to 0 replicas")
By(fmt.Sprintf("cordoning node %s", node))
```

Not a header (`By("current pod->node placement:")`), not an assertion
(`By("BGP is still up")`), not past tense.

### Raw output and measurements

- Free-form dumps (logs, command output) go to `GinkgoWriter`.
- Measured values go to `AddReportEntry`, so they are structured entries in the
  JUnit XML rather than free text:

```go
AddReportEntry("bfd-detection-time", bfdDetect.Round(time.Millisecond))
```

- `utils.Run` logs the command only when `E2E_TRACE=1` is set. Under 2-second
  `Eventually` polling, logging every `kubectl` invocation buried the actual
  failure in hundreds of identical lines.

### Failure diagnostics

Every suite dumps data-plane diagnostics on failure, not just a few:

```go
AfterEach(func() {
    if CurrentSpecReport().Failed() {
        GinkgoWriter.Print(e2eutils.DataPlaneDiagnostics{...}.Collect())
    }
})
```

## 4. Timeouts

`SetDefaultEventuallyTimeout` and `SetDefaultEventuallyPollingInterval` are
**global and must be set in exactly one place** — `TestE2E`, from the constants
in `e2e_conventions_test.go`.

Calling them inside a container body does not scope them to that container:
container bodies all run during tree construction, so the calls are global
last-writer-wins. Suites that did this had their intended budgets silently
overwritten by whichever file happened to be initialized last.

Per-spec deviations are explicit at the assertion:

```go
Eventually(...).WithTimeout(30 * time.Second).Should(Succeed())
```

## 5. Adding a suite

1. Add the topology and its `deploy-`/`undeploy-` targets to the `Makefile`
   (see `suites/README.md` for addressing and VLAN allocation). Put the block in
   the `##@ IPv4 / Dual-stack / IPv6 suites` section for its family: suites with
   their own topology first, then suites reusing a shared topology, each under a
   `#---` divider naming the target.
2. Add a `labelSuite<Name>` constant to the registry in
   `e2e_conventions_test.go` and list it in `allSuiteLabels`.
3. Add a `test-<name>` target using `$(call run-specs,suite:<name>)`.
4. If the suite should run in CI, add it to `SUITES_IPV4`, `SUITES_DS` or
   `SUITES_IPV6` and give the tree the matching IP-family label.
5. Write the tree per sections 1–4 above.

Makefile help descriptions follow one grammar, so `make help` reads uniformly:

```
<suite>            Deploy and test <suite> suite.
deploy-<suite>     Deploy <topology> topology[ (shared)].
test-<suite>       Run e2e tests for <suite> suite[ (what it covers)].
undeploy-<suite>   Undeploy <topology> topology[ (shared)].
```

Name the *target* in the description, never a prose variant (`bgp suite`, not
`BGP VIP advertisement suite`) — the target name is what the reader types. For
suites reusing another suite's topology, name the topology actually deployed and
mark it `(shared)`, so it is obvious that `undeploy-` tears down infrastructure
other suites also use. The help column is 37 wide, which fits the longest target
name (`undeploy-separate-static-appnetwork`, 35) plus a two-space gutter.

Section order is `General`, `Cluster setup`, `IPv4 suites`, `IPv6 suites`,
`Dual-stack suites`, `CI aggregates`, `OpenShift CRC`. The IP-family order
(IPv4, IPv6, dual-stack) is the same in the `SUITES_*` variables and in the
`e2e.yaml` CI matrix.

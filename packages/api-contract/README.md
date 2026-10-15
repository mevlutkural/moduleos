# ModuleOS API contract

`openapi.yaml` is the canonical machine-readable contract for public ModuleOS
HTTP behavior. A route is added here in the same pull request as its runtime
implementation; undocumented runtime routes and unimplemented contract routes
are rejected by tests.

Validate the contract and its parity with the control-plane router from the
repository root:

```sh
go test -count=1 ./packages/api-contract/... ./apps/control-plane/internal/api/...
./scripts/check-openapi.sh
```

System health routes are intentionally unauthenticated. Protected resource
routes use the Bearer scheme when those vertical slices are introduced.

Live application logs use authenticated `text/event-stream` responses. Native
browser `EventSource` cannot attach the required Bearer header, so browser
clients must use authenticated fetch streaming. Streams are ephemeral and
bounded: they expose no event ID or replay cursor, reject `Last-Event-ID`, and
may repeat the configured initial tail after reconnecting.

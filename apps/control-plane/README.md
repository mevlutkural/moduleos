# ModuleOS Control Plane

The control plane is the long-running ModuleOS backend. It owns application use
cases, desired state, reconciliation, runtime adapters, persistence, and the
public API transport.

The HTTP API is an adapter within this application; it is not a separate
application or the identity of the control plane.

## Development

Run the control-plane test suite from the repository root:

```sh
go test ./apps/control-plane/...
./scripts/check-coverage.sh
```

The production daemon built from this application is named `moduleosd`.

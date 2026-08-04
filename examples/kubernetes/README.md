# Atryum On Kubernetes / OpenShift

> Note: this is not a production ready installation as it leaves authentication off

This example deploys:

- `validmind/atryum:latest` from Docker Hub
- PostgreSQL on `quay.io/sclorg/postgresql-16-c9s:latest`, which is built for OpenShift-style deployments
- a Service and Ingress for Atryum
- the `atryum.toml` config mounted from a Kubernetes Secret

## Files

- `atryum-openshift.yaml` — full stack example with Atryum, PostgreSQL, PVC, Service, and Ingress

## Before You Apply It

1. Change `public_base_url` and the Ingress host from `atryum.example.com` to your real hostname.
2. Replace the placeholder PostgreSQL passwords in the Secrets.
3. If your cluster does not have a default `StorageClass`, set `spec.storageClassName` on the PVC.

## Apply

```sh
kubectl apply -n your-namespace -f examples/kubernetes/atryum-openshift.yaml
```

## If You Want The OpenShift Catalog PostgreSQL Instead

If you already provisioned Red Hat's `PostgreSQL` or `PostgreSQL (Ephemeral)` from the OpenShift Software Catalog, you can still use this example:

1. Skip the `atryum-postgres-*` resources in `atryum-openshift.yaml`.
2. Point `server.database_url` in the `atryum-config` Secret at the catalog service, for example:

```toml
database_url = "postgresql://atryum:replace-me@postgresql:5432/atryum?sslmode=disable"
```

Use the actual service name, database name, user, and password created by your catalog instance.

## Important Auth Note

The example keeps auth minimal so it starts cleanly, but Atryum's admin UI/API should not be exposed publicly without configuring at least one real `[[auth]]` block with `admin_enabled = true` in `atryum.toml`.

Until you do that, put the Ingress behind another trusted access layer or keep it internal.

## OpenShift Route Alternative

If you prefer an OpenShift `Route` instead of a standard Kubernetes `Ingress`, you can expose the service with:

```sh
oc expose service/atryum
```

Then update `public_base_url` to the route hostname.

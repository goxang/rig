# Manifests

`rig manifests <folder>` (and the TUI's Manifests screen) reads **any** layout: every `.yml`/`.yaml`
below the folder, multi-document files, `List`s, and kustomizations (rendered with `kubectl kustomize`).
Files that are not Kubernetes YAML, such as Helm templates, are noted and skipped.

It links objects, for example `Service → Deployment` (selector), `Ingress → Service`,
`HPA → Deployment`, `Deployment → ConfigMap / Secret / PVC / ServiceAccount`, `PDB / NetworkPolicy →
pods`, and `RoleBinding → Role / ServiceAccount`. It then flags what is wrong: references to objects
that are not there, Services that select nothing, unpinned images, and containers without requests or
readiness probes.

## A layout that works well

```
deploy/
  infra/
    prometheus/          prometheus.yaml (ConfigMap + Deployment + Service)
    postgres/
  services/
    api/
      deployment.yaml
      service.yaml
      config.yaml        ConfigMaps the service reads
      hpa.yaml
    worker/
      deployment.yaml    several objects in one file is fine too
  overlays/              optional kustomize overlays per environment
    kind/kustomization.yaml
```

Point an environment at it with `runtime.manifests: [deploy]`.

## How deploy uses it

For each service, rig finds its workload: `k8s.workload` (`deployment/api`), else a workload named
after the service, else one labelled `app.kubernetes.io/name` or `app` with that name. It applies that
workload **plus everything that exists for it**: the Services selecting it, its HPA and PDB, and the
ConfigMaps, Secrets, PVCs and ServiceAccount it uses.

While rendering it:

- substitutes `$VAR` / `${VAR}` from `runtime.vars`, then `TAG`, `REGISTRY` and `NAMESPACE`;
- sets the image of the service's container (`k8s.container`, default the first) to the one just built;
- adds `runtime.env` and the service's `env` to every container, replacing entries of the same name;
- labels the workload `app.kubernetes.io/managed-by: rig`.

A service without manifests still deploys: rig generates a Deployment and a Service from its `image`,
`ports`, `env`, `health` and `replicas`. That is the quickest way to run infrastructure such as Redis.

`rig do runtime render <service>` prints exactly what deploy would apply.

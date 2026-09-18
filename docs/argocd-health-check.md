# Argo CD health check

Argo CD has no built-in health assessment for `TigerBeetleCluster`, so without this check a
failing cluster never shows as Degraded. The operator reports health through kstatus-compatible
conditions (`Ready`, `Reconciling`, `Stalled`) and `status.observedGeneration`; this check maps
them onto Argo CD health.

Add it to the `argocd-cm` ConfigMap:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
data:
  resource.customizations.health.tigerbeetle.codegrowers.com_TigerBeetleCluster: |
    local hs = {}
    if obj.status == nil or obj.status.conditions == nil then
      hs.status = "Progressing"
      hs.message = "Waiting for the operator to reconcile"
      return hs
    end
    if obj.status.observedGeneration == nil or obj.status.observedGeneration < obj.metadata.generation then
      hs.status = "Progressing"
      hs.message = "Waiting for the operator to observe the latest spec"
      return hs
    end
    local ready, reconciling, stalled
    for _, c in ipairs(obj.status.conditions) do
      if c.type == "Ready" then ready = c end
      if c.type == "Reconciling" then reconciling = c end
      if c.type == "Stalled" then stalled = c end
    end
    if stalled ~= nil and stalled.status == "True" then
      hs.status = "Degraded"
      hs.message = stalled.reason .. ": " .. stalled.message
      return hs
    end
    if reconciling ~= nil and reconciling.status == "True" then
      hs.status = "Progressing"
      hs.message = reconciling.reason .. ": " .. reconciling.message
      return hs
    end
    if ready ~= nil and ready.status == "True" then
      hs.status = "Healthy"
      hs.message = ready.message
      return hs
    end
    hs.status = "Progressing"
    hs.message = ready ~= nil and (ready.reason .. ": " .. ready.message) or "Not ready"
    return hs
```

Events are not used for health, but every error and lifecycle milestone is also recorded as a
Kubernetes Event on the `TigerBeetleCluster`, visible in the Argo CD "Events" tab and in
`kubectl describe tigerbeetlecluster <name>`.

Flux (`healthChecks`) and `kubectl wait --for=condition=Ready` work with the same conditions
without extra configuration.

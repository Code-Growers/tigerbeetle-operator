# TigerBeetle Operator — Tilt development environment
# Usage: make kind-up && make tilt-up

# Only ever deploy to the project's kind cluster, regardless of the current kubectl context.
expected_context = 'kind-' + os.getenv('KIND_CLUSTER', 'tigerbeetle-operator')
if k8s_context() != expected_context:
    fail("Refusing to run against context '%s'; expected '%s'. Use `make tilt-up` or `tilt up --context %s`." % (
        k8s_context(), expected_context, expected_context))

update_settings(max_parallel_updates=5)

operator = load_dynamic('./tilt/operator.Tiltfile')
operator['configure']()

cfg = config.parse()
operator['start'](cfg)

load('ext://uibutton', 'cmd_button')
load('ext://helm_resource', 'helm_resource')

def configure():
    config.define_bool(
        "operator",
        args=False,
        usage="Deploy the TigerBeetle operator",
    )

def start(cfg):
    if not cfg.get("operator", False):
        return

    cmd_button(
        'operator:test',
        argv=['sh', '-c', 'go test ./...'],
        resource='operator',
        icon_name='repeat',
        text='Run tests',
    )

    docker_build(
        'localhost:5001/tigerbeetle-operator',
        '.',
        dockerfile='./docker/Dockerfile',
        only=[
            './api',
            './cmd/main.go',
            './internal',
            './go.mod',
            './go.sum',
        ],
    )

    docker_build(
        'localhost:5001/tigerbeetle-operator-init',
        '.',
        dockerfile='./docker/Dockerfile.init',
        only=[
            './api',
            './cmd/init',
            './internal/tigerbeetle',
            './go.mod',
            './go.sum',
        ],
    )

    helm_resource(
        'operator',
        chart='./helm/tigerbeetle-operator',
        release_name='tigerbeetle-operator',
        namespace='tigerbeetle-operator-system',
        deps=[
            './helm/tigerbeetle-operator',
        ],
        image_deps=[
            'localhost:5001/tigerbeetle-operator',
            'localhost:5001/tigerbeetle-operator-init',
        ],
        image_keys=[
            ('image.repository', 'image.tag'),
            ('initImage.repository', 'initImage.tag'),
        ],
        flags=[
            '--create-namespace',
        ],
        labels=["operator"],
    )

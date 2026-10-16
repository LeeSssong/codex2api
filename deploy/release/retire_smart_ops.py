"""Prepare resolved Compose configuration for the smart-ops retirement release.

Consume docker compose config --format json output; never print resolved secrets.
The caller owns stopping writers, database migration and health verification.
"""
import copy

def retire_compose(source, image):
    result = copy.deepcopy(source)
    services = result['services']
    app = services['codex2api']
    if app.get('env_file'):
        raise ValueError('resolve environment files before retiring smart operations')
    worker = services.get('credential-runtime')
    if worker is not None:
        if worker.get('container_name') != 'codex2api-credential-runtime':
            raise ValueError('reserved service name belongs to an unrelated container')
        del services['credential-runtime']
    env = app.get('environment', {})
    if not isinstance(env, dict):
        raise ValueError('resolved environment must be a mapping')
    app['environment'] = {k: v for k, v in env.items() if not k.startswith('CODEX2API_CREDENTIAL_OPS_')}
    app['image'] = image
    return result

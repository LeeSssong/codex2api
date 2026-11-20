"""Derive a runtime from a verified immutable bundle without downloading dependencies."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil

OWNED = ('worker.py', 'engines.py', 'openai_siwc_oauth_adapter.py',
         'openai_siwc_password_flow.mjs', 'SOURCE-MANIFEST.json')


def derive(baseline, expected, source, destination, revision):
    baseline, source, destination = map(Path, (baseline, source, destination))
    raw = (baseline / 'manifest.json').read_bytes()
    if hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError('baseline manifest checksum mismatch')
    manifest = json.loads(raw)
    for name, digest in manifest['files'].items():
        path = baseline / name
        if Path(name).is_absolute() or '..' in Path(name).parts or path.is_symlink() or not path.resolve().is_relative_to(baseline.resolve()):
            raise ValueError('unsafe baseline path')
        if hashlib.sha256(path.read_bytes()).hexdigest() != digest:
            raise ValueError('baseline file checksum mismatch')
    if destination.exists():
        raise ValueError('destination already exists')
    destination.mkdir(parents=True, mode=0o700)
    # Copy only manifested files; never carry untracked mutable state.
    for name in manifest['files']:
        target = destination / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(baseline / name, target)
    for name in OWNED:
        shutil.copy2(source / name, destination / name)
    manifest['parent_manifest_sha256'] = expected
    manifest['application_revision'] = revision
    manifest['derivation'] = 'Verified dependency bundle with application-owned SIWC adapters'
    manifest['files'] = {str(path.relative_to(destination)): hashlib.sha256(path.read_bytes()).hexdigest()
                         for path in destination.rglob('*') if path.is_file()}
    output = (json.dumps(manifest, sort_keys=True, indent=2) + '\n').encode()
    (destination / 'manifest.json').write_bytes(output)
    return hashlib.sha256(output).hexdigest()


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('baseline', 'expected', 'source', 'destination', 'revision'):
        parser.add_argument('--' + name, required=True)
    args = parser.parse_args()
    print(derive(args.baseline, args.expected, args.source, args.destination, args.revision))

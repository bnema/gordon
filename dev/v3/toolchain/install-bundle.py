#!/usr/bin/env python3
"""Install a verified development bundle into its isolated VM prefix."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import tarfile
import tempfile

PREFIX = Path('/opt/gordon-v3-toolchain')


def sha(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive', type=Path)
    parser.add_argument('sha256')
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise RuntimeError('administrator provisioning is required; never run through Gordon')
    if sha(args.archive) != args.sha256:
        raise RuntimeError('archive checksum mismatch')
    if PREFIX.exists() or PREFIX.is_symlink():
        raise RuntimeError('prefix already exists; refuse replacement')
    with tempfile.TemporaryDirectory(prefix='gordon-toolchain-', dir=PREFIX.parent) as tmp:
        stage = Path(tmp) / 'payload'
        stage.mkdir()
        with tarfile.open(args.archive) as archive:
            members = archive.getmembers()
            names = set()
            total = 0
            for member in members:
                path = Path(member.name)
                if path.is_absolute() or '..' in path.parts or member.name in names:
                    raise RuntimeError('unsafe or duplicate archive path')
                if not (member.isfile() or member.isdir()) or member.mode & 0o7000:
                    raise RuntimeError('unsupported archive member')
                names.add(member.name)
                total += member.size
            if total > 1024 ** 3:
                raise RuntimeError('archive exceeds development bundle bound')
            archive.extractall(stage, filter='data')
        manifest = json.loads((stage / 'manifest.json').read_text())
        if manifest['format'] != 1 or manifest['prefix'] != str(PREFIX):
            raise RuntimeError('incompatible manifest')
        actual = {str(p.relative_to(stage)): sha(p) for p in stage.rglob('*')
                  if p.is_file() and p.name != 'manifest.json'}
        if actual != manifest['files']:
            raise RuntimeError('bundle inventory mismatch')
        shutil.copyfile(Path(__file__).with_name('containers.conf'), stage / 'containers.conf')
        (stage / 'storage.conf').write_text('[storage]\ndriver = "overlay"\n')
        stage.chmod(0o755)
        stage.rename(PREFIX)
    print('Installed isolated development bundle; packaged Podman is unchanged.')


if __name__ == '__main__':
    main()

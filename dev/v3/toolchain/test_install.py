"""Narrow installer refusal and inventory checks without host mutation."""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('installer', Path(__file__).with_name('install-bundle.py'))
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


class InstallTests(unittest.TestCase):
    def test_archive_validation(self):
        for case in ('valid', 'checksum', 'traversal', 'symlink', 'inventory', 'existing'):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                prefix = root / 'installed'
                archive = root / 'bundle.tar'
                with tarfile.open(archive, 'w') as out:
                    data = b'fixture'
                    member = tarfile.TarInfo('../escape' if case == 'traversal' else 'bin/tool')
                    member.size = len(data)
                    if case == 'symlink':
                        member.type = tarfile.SYMTYPE
                        member.linkname = '/etc/passwd'
                        member.size = 0
                    out.addfile(member, io.BytesIO(data))
                    manifest = json.dumps({'format': 1, 'prefix': str(prefix), 'files': {
                        'bin/tool': 'wrong' if case == 'inventory' else hashlib.sha256(data).hexdigest()
                    }}).encode()
                    member = tarfile.TarInfo('manifest.json')
                    member.size = len(manifest)
                    out.addfile(member, io.BytesIO(manifest))
                if case == 'existing':
                    prefix.mkdir()
                expected = 'wrong' if case == 'checksum' else installer.sha(archive)
                with patch.object(installer, 'PREFIX', prefix), patch.object(installer.os, 'geteuid', return_value=0), patch('sys.argv', ['install', str(archive), expected]):
                    if case == 'valid':
                        installer.main()
                        self.assertEqual((prefix / 'bin/tool').read_bytes(), b'fixture')
                    else:
                        with self.assertRaises(RuntimeError):
                            installer.main()
                self.assertFalse((root / 'escape').exists())


if __name__ == '__main__':
    unittest.main()

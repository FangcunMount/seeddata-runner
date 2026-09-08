"""Upgrade only IAM paths in retained seeddata deployment configuration."""
import argparse
import os
from pathlib import Path
import shutil
import time


def upgraded_config(text):
    lines = []
    in_iam = False
    for line in text.splitlines(keepends=True):
        if line and not line[0].isspace() and not line.startswith('#'):
            in_iam = line.strip() == 'iam:'
        if in_iam:
            if line.startswith('  tenantId:'):
                continue
            line = line.replace('/api/v2/authn/', '/api/v3/authn/').replace('/api/v2/internal/authn/', '/api/v3/internal/authn/')
        lines.append(line)
    return ''.join(lines)


def upgraded_env(text):
    lines = []
    for line in text.splitlines(keepends=True):
        if line.startswith('SEEDDATA_IAM_LOGIN_URL='):
            line = line.replace('/api/v2/authn/', '/api/v3/authn/')
        lines.append(line)
    return ''.join(lines)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    changed = []
    for relative, transform in [('config/seeddata.yaml', upgraded_config), ('env/seeddata.env', upgraded_env)]:
        path = args.root / relative
        if path.is_symlink() or not path.is_file():
            raise SystemExit('invalid runtime configuration file')
        original = path.read_text()
        updated = transform(original)
        if original != updated:
            changed.append((path, updated))
    if args.apply and changed:
        backup = args.root / 'backups' / ('iam-v5-config-' + str(time.time_ns()))
        backup.mkdir(parents=True, mode=0o700)
        for path, updated in changed:
            shutil.copy2(path, backup / path.name)
            os.chmod(backup / path.name, 0o600)
            temporary = path.with_suffix(path.suffix + '.iam-v5-incoming')
            with open(temporary, 'x') as output:
                os.chmod(temporary, 0o600)
                output.write(updated)
            os.replace(temporary, path)
    print('IAM v5 configuration: mode=%s changed_files=%d' % ('apply' if args.apply else 'preview', len(changed)))


if __name__ == '__main__':
    main()

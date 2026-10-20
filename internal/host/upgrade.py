"""In-place controller transaction. B and I are embedded bootstrap/installer modules.
Only this controller is replaced. Backend containers are never stopped/recreated.
"""
import contextlib
import io
import shutil
import stat
import time


def durable(path, value):
    fd, temporary = tempfile.mkstemp(prefix='.upgrade-', dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as output:
            json.dump(value, output)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        sync(path.parent)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def sync(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def private(path):
    if path.is_symlink() or not path.is_dir() or path.stat().st_uid != os.getuid() or path.stat().st_mode & 0o077:
        raise SetupError('storage')


def regular_tree(path):
    for item in (path, *path.rglob('*')):
        mode = item.lstat().st_mode
        if not (stat.S_ISDIR(mode) or stat.S_ISREG(mode)) or item.stat().st_uid != os.getuid():
            raise SetupError('storage')


def backends():
    return sorted(docker('ps', '-aq', '--filter', 'label=io.vpnmodo.managed-by=vpnmodo', '--filter', 'label=io.vpnmodo.profile').decode().split())


def ready(expected=None):
    for _ in range(60):
        try:
            docker('exec', NAME, '/usr/local/bin/vpnmodo-controller', 'health')
            if expected and docker('exec', NAME, '/usr/local/bin/vpnmodo-controller', 'version').decode().strip() != expected:
                raise SetupError('version')
            return
        except SetupError:
            time.sleep(0.5)
    raise SetupError('readiness')


def validate_controller(root):
    old = inspect('container', NAME)
    labels = (old or {}).get('Config', {}).get('Labels') or {}
    if labels.get(MANAGER) != 'vpnmodo' or labels.get(COMPONENT) != 'controller' or labels.get('com.docker.compose.project') != NAME:
        raise SetupError('ownership')
    if not any(m.get('Type') == 'bind' and m.get('Source') == str(root) and m.get('Destination') == str(root) for m in old.get('Mounts', [])):
        raise SetupError('ownership')
    if not old['State']['Running'] or old['State'].get('Paused'):
        raise SetupError('not_running')
    return old


def restore(root, txn, record):
    phase = record['phase']
    if phase in ('staging', 'paused'):
        old = inspect('container', NAME)
        if old and old['State'].get('Paused'):
            docker('unpause', NAME)
    elif phase in ('activating', 'prepared'):
        if inspect('container', NAME):
            docker('rm', '--force', NAME)
        state = root / 'controller-state'
        if state.exists():
            regular_tree(state)
            shutil.rmtree(state)
        shutil.copytree(txn / 'state', state)
        shutil.copyfile(txn / 'controller.compose.json', root / 'controller.compose.json')
        docker('compose', '--project-name', NAME, '-f', str(root / 'controller.compose.json'), 'up', '-d', '--no-deps', 'controller')
    if (root / 'upgrade-pending').exists():
        (root / 'upgrade-pending').unlink()
        sync(root)
    ready()
    record['phase'] = 'rolled_back'
    durable(txn / 'transaction.json', record)


def prune(history):
    completed = []
    for directory in history.iterdir():
        if not re.fullmatch('[0-9a-f]{32}', directory.name):
            continue
        private(directory)
        if json.loads((directory / 'transaction.json').read_text())['phase'] in ('committed', 'rolled_back'):
            completed.append(directory)
    for directory in sorted(completed, key=lambda p: p.stat().st_mtime, reverse=True)[2:]:
        regular_tree(directory)
        shutil.rmtree(directory)
    sync(history)


def upgrade(params):
    if set(params) - {'action', 'token', 'runtime'} or params.get('action') not in ('prepare', 'commit', 'rollback', 'status') or not re.fullmatch('[0-9a-f]{32}', params.get('token', '')):
        raise SetupError('input')
    root = Path.home() / '.local/share/vpnmodo'
    for parent in (root, *root.parents):
        if parent.is_symlink():
            raise SetupError('storage')
    private(root)
    if params['action'] == 'prepare':
        private(root / 'controller-state')
    history = root / 'upgrades'
    history.mkdir(mode=0o700, exist_ok=True)
    private(history)
    sync(root)
    fd = os.open(root / '.setup.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'a+b') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        txn = history / params['token']
        action = params['action']
        marker = root / 'upgrade-pending'
        if action != 'prepare':
            if not txn.exists():
                if action in ('status', 'rollback'):
                    return {'ok': True, 'phase': 'absent'}
                raise SetupError('transaction')
            private(txn)
            record = json.loads((txn / 'transaction.json').read_text())
            if record['phase'] in ('committed', 'rolled_back'):
                if record['phase'] == 'committed' and marker.exists():
                    if json.loads(marker.read_text())['token'] != params['token']:
                        raise SetupError('busy')
                    marker.unlink()
                    sync(root)
                if action == 'rollback' and record['phase'] == 'committed':
                    raise SetupError('already_committed')
                return {'ok': True, 'phase': record['phase']}
            if marker.exists() and json.loads(marker.read_text())['token'] != params['token']:
                raise SetupError('busy')
            if action == 'commit':
                if record['phase'] != 'prepared':
                    raise SetupError('transaction')
                ready(record['version'])
                if backends() != record['backends']:
                    raise SetupError('backends_changed')
                # Persist intent first. Repeated commit must remove a stale marker too.
                record['phase'] = 'committed'
                durable(txn / 'transaction.json', record)
                marker.unlink()
                sync(root)
            elif action == 'rollback':
                restore(root, txn, record)
            if record['phase'] in ('committed', 'rolled_back'):
                prune(history)
            return {'ok': True, 'phase': record['phase']}
        if marker.exists() or txn.exists():
            raise SetupError('busy')
        # Do not overwrite a transaction interrupted before its barrier was created.
        for prior in history.glob('*/transaction.json'):
            if json.loads(prior.read_text())['phase'] not in ('committed', 'rolled_back'):
                raise SetupError('busy')
        old = validate_controller(root)
        regular_tree(root / 'controller-state')
        manifest = root / 'controller.compose.json'
        if manifest.is_symlink() or not manifest.is_file():
            raise SetupError('storage')
        txn.mkdir(mode=0o700)
        sync(history)
        record = {'phase': 'staging', 'version': params['runtime']['binary_version'], 'backends': backends()}
        durable(txn / 'transaction.json', record)
        try:
            durable(txn / 'runtime.json', params['runtime'])
            captured = io.StringIO()
            with contextlib.redirect_stdout(captured):
                I['install_registry'](txn / 'runtime.json')
            images = json.loads(captured.getvalue())
            shutil.copyfile(manifest, txn / 'controller.compose.json')
            os.chmod(txn / 'controller.compose.json', 0o600)
            with (txn / 'controller.compose.json').open('rb') as stream:
                os.fsync(stream.fileno())
            # Persist phase before pausing so an interrupted SSH call can recover.
            record['phase'] = 'paused'
            durable(txn / 'transaction.json', record)
            docker('pause', NAME)
            shutil.copytree(root / 'controller-state', txn / 'state')
            regular_tree(txn / 'state')
            for item in (txn / 'state').rglob('*'):
                if item.is_file():
                    with item.open('rb') as stream:
                        os.fsync(stream.fileno())
                elif item.is_dir():
                    sync(item)
            sync(txn / 'state')
            sync(txn)
            docker('run', '--rm', '--pull=never', '--network', 'none', '--user', f'{os.getuid()}:{os.getgid()}', '--mount', f'type=bind,src={txn / "state"},dst=/snapshot', images['controller_image'], 'upgrade-check', '--state-dir', '/snapshot')
            durable(marker, {'token': params['token']})
            record['phase'] = 'activating'
            durable(txn / 'transaction.json', record)
            docker('rm', '--force', NAME)
            activate({'image': images['controller_image'], 'replace': True, 'backend_images': images['backend_images']}, root, root / 'controller-state')
            ready(record['version'])
            if backends() != record['backends']:
                raise SetupError('backends_changed')
            record['phase'] = 'prepared'
            durable(txn / 'transaction.json', record)
        except Exception:
            restore(root, txn, record)
            raise SetupError('activation') from None
        return {'ok': True, 'phase': 'prepared'}


def upgrade_main():
    try:
        payload = sys.stdin.buffer.read(16385)
        if len(payload) > 16384:
            raise SetupError('input')
        result = upgrade(json.loads(payload))
    except Exception:
        result = {'ok': False, 'error': 'upgrade'}
    print(json.dumps(result))


if __name__ == '__main__':
    upgrade_main()

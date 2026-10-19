#!/usr/bin/env python3
"""Native hidden credential/MFA input, idle wait, cancellation and echo checks."""
import errno
import fcntl
import os
from pathlib import Path
import pty
import select
import subprocess
import tempfile
import termios
import time


def run(binary, mode):
    master, slave = pty.openpty()
    previous = termios.tcgetattr(slave)
    def controlling_tty():
        os.setsid()
        fcntl.ioctl(0, termios.TIOCSCTTY, 0)
    process = subprocess.Popen([str(binary), '-test.run=^TestTerminalHelper$', '-test.v'],
                               stdin=slave, stdout=slave, stderr=slave,
                               env=dict(os.environ, VPNMODO_SYNTHETIC_TERMINAL=mode),
                               preexec_fn=controlling_tty)
    transcript = bytearray()
    sent = set()
    restored = False
    prompts = [(b'MFA code: ', b'123456')] if mode != 'credentials' else [
        (b'Username: ', b'synthetic-user'), (b'Password: ', b'synthetic-password'),
        (b'Pre-shared key: ', b'synthetic-shared')]
    try:
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if select.select([master], [], [], 0.05)[0]:
                try:
                    data = os.read(master, 8192)
                except OSError as e:
                    if e.errno != errno.EIO:
                        raise
                    data = b''
                if not data:
                    break
                transcript.extend(data)
            for prompt, value in prompts:
                if prompt in transcript and prompt not in sent:
                    assert not termios.tcgetattr(slave)[3] & termios.ECHO
                    if mode != 'cancel':
                        time.sleep(0.15)
                        assert process.poll() is None, 'prompt exited while awaiting input'
                        os.write(master, value + b'\r')
                    sent.add(prompt)
            if b'TERMINAL_RESTORED' in transcript and not restored:
                assert termios.tcgetattr(slave) == previous, 'terminal settings not restored'
                os.write(master, b'\n')
                restored = True
            if process.poll() is not None:
                break
        assert process.wait(timeout=2) == 0, transcript.decode(errors='replace')
        assert len(sent) == len(prompts), 'missing hidden prompt'
        assert all(value not in transcript for _, value in prompts), 'input echoed'
        assert restored, 'missing restored-terminal acknowledgement'
    finally:
        if process.poll() is None:
            process.kill()
            process.wait(timeout=2)
        os.close(master)
        os.close(slave)


def main():
    source = Path(__file__).resolve().parents[1]
    with tempfile.TemporaryDirectory(prefix='vpnmodo-terminal-') as directory:
        binary = Path(directory) / 'input.test'
        subprocess.run(['go', 'test', '-c', '-o', str(binary), './internal/credentialinput'], cwd=source, check=True)
        for mode in ('code', 'credentials', 'cancel'):
            run(binary, mode)
    print('PASS: native hidden MFA/credential prompts wait, suppress echo and restore terminal after completion/cancellation')


if __name__ == '__main__':
    main()

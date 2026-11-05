"""Live packet-size assertions for explicitly authorized synthetic Linux labs."""
from smoke_controller import command


def assert_ipsec_network(client, mtu=1300):
    routes = command(['docker', 'exec', client, 'ip', '-o', '-4', 'route', 'show', 'table', '220']).stdout.decode().splitlines()
    traffic = [line.split() for line in routes if line.split() and line.split()[0] not in ('throw', 'unreachable', 'blackhole', 'prohibit')]
    assert traffic, 'missing VPN traffic routes'
    for fields in traffic:
        assert 'mtu' in fields and fields[fields.index('mtu') + 1] == str(mtu), 'VPN route MTU missing or inconsistent'
        assert 'advmss' in fields and fields[fields.index('advmss') + 1] == str(mtu - 40), 'VPN route MSS missing or inconsistent'
    # Native route metrics must not reduce the underlying transport interface.
    links = command(['docker', 'exec', client, 'ip', '-o', 'link', 'show', 'dev', 'eth0']).stdout.decode().split()
    assert int(links[links.index('mtu') + 1]) == 1500, 'outer transport interface MTU changed'

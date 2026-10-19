"""Tiny synthetic PAP verifier, never use as a production AAA server."""
import hashlib
import socket
import struct

SECRET = b"synthetic-radius"
sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
sock.bind(("127.0.0.1", 1812))
while True:
    packet, peer = sock.recvfrom(4096)
    if len(packet) < 20 or packet[0] != 1:
        continue
    code, identifier, length = struct.unpack("!BBH", packet[:4])
    auth = packet[4:20]
    attrs = {}
    offset = 20
    while offset + 2 <= min(length, len(packet)):
        kind, size = packet[offset:offset+2]
        if size < 2 or offset + size > length:
            break
        attrs[kind] = packet[offset+2:offset+size]
        offset += size
    encoded = attrs.get(2, b"")
    password = b""
    previous = auth
    for offset in range(0, len(encoded), 16):
        block = encoded[offset:offset+16]
        digest = hashlib.md5(SECRET + previous).digest()
        password += bytes(a ^ b for a, b in zip(block, digest))
        previous = block
    valid = attrs.get(1) == b"synthetic-user" and password.rstrip(b"\0") == b"synthetic-password9876543210"
    head = struct.pack("!BBH", 2 if valid else 3, identifier, 20)
    sock.sendto(head + hashlib.md5(head + auth + SECRET).digest(), peer)

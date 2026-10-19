"""Reachable plaintext escape sink and tunnel TCP endpoint in the test gateway."""
import socket
import threading

def tcp():
    sock = socket.socket()
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.bind(("0.0.0.0", 28080))
    sock.listen()
    def echo(conn):
        with conn:
            while True:
                data = conn.recv(4096)
                if not data:
                    return
                conn.sendall(data)
    while True:
        conn, _ = sock.accept()
        threading.Thread(target=echo, args=(conn,), daemon=True).start()
threading.Thread(target=tcp, daemon=True).start()
sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
sock.bind(("0.0.0.0", 28081))
while True:
    data, peer = sock.recvfrom(4096)
    sock.sendto(data, peer)

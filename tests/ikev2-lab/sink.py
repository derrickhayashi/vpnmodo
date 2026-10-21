#!/usr/bin/python3
import socket
import threading


def echo(conn):
    with conn:
        while True:
            data = conn.recv(65536)
            if not data:
                return
            conn.sendall(data)


sock = socket.socket()
sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
sock.bind(('198.51.100.1', 8080))
sock.listen()
while True:
    conn, _ = sock.accept()
    threading.Thread(target=echo, args=(conn,), daemon=True).start()

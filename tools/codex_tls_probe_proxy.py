#!/usr/bin/env python3
"""Capture a single TLS ClientHello through a local HTTP CONNECT proxy.

This never forwards traffic, decrypts requests, or accepts credentials. Use
only with deliberately unauthenticated diagnostics on a controlled machine.
"""

import argparse
import hashlib
import json
import socket
import struct
import sys
import time


def take(buf, offset, length):
    if offset + length > len(buf):
        raise ValueError("truncated ClientHello")
    return buf[offset:offset + length], offset + length


def u16(buf, offset):
    value, offset = take(buf, offset, 2)
    return struct.unpack(">H", value)[0], offset


def is_grease(n):
    return ((n >> 8) == (n & 0xFF)) and ((n & 0x0F0F) == 0x0A0A)


def parse_hello(handshake):
    if handshake[0] != 1:
        raise ValueError("expected TLS ClientHello")
    length = int.from_bytes(handshake[1:4], "big")
    data = handshake[4:4 + length]
    offset = 0
    version, offset = u16(data, offset)
    _, offset = take(data, offset, 32)
    sid_length = data[offset]
    _, offset = take(data, offset + 1, sid_length)
    ciphers_length, offset = u16(data, offset)
    ciphers_raw, offset = take(data, offset, ciphers_length)
    ciphers = [int.from_bytes(ciphers_raw[i:i + 2], "big") for i in range(0, len(ciphers_raw), 2)]
    comp_length = data[offset]
    _, offset = take(data, offset + 1, comp_length)
    extensions_length, offset = u16(data, offset)
    extension_raw, offset = take(data, offset, extensions_length)
    exts = []
    values = {}
    pos = 0
    while pos < len(extension_raw):
        kind, pos = u16(extension_raw, pos)
        size, pos = u16(extension_raw, pos)
        val, pos = take(extension_raw, pos, size)
        exts.append(kind)
        values[kind] = val

    groups = []
    if 10 in values:
        group_bytes = values[10]
        n, _ = u16(group_bytes, 0)
        groups = [int.from_bytes(group_bytes[i:i + 2], "big") for i in range(2, 2 + n, 2)]
    ec_points = []
    if 11 in values:
        b = values[11]
        ec_points = list(b[1:1 + b[0]])
    alpn = []
    if 16 in values:
        b = values[16][2:]
        while b:
            n = b[0]
            alpn.append(b[1:1 + n].decode("ascii", errors="replace"))
            b = b[1 + n:]
    versions = []
    if 43 in values:
        b = values[43]
        versions = [int.from_bytes(b[i:i + 2], "big") for i in range(1, 1 + b[0], 2)]
    ja3 = ",".join(
        [str(version), "-".join(map(str, filter(lambda x: not is_grease(x), ciphers))),
         "-".join(map(str, filter(lambda x: not is_grease(x), exts))),
         "-".join(map(str, filter(lambda x: not is_grease(x), groups))),
         "-".join(map(str, ec_points))]
    )
    return {
        "legacy_version": version, "cipher_count": len(ciphers), "ciphers": ciphers,
        "extensions": exts, "groups": groups, "point_formats": ec_points,
        "alpn": alpn, "supported_versions": versions,
        "sni_present": 0 in values, "ja3": ja3,
        "ja3_md5": hashlib.md5(ja3.encode("ascii")).hexdigest(),
    }


def read_exact(sock, count):
    parts = bytearray()
    while len(parts) < count:
        data = sock.recv(count - len(parts))
        if not data:
            raise ConnectionError("client disconnected")
        parts.extend(data)
    return bytes(parts)


def capture(conn):
    conn.settimeout(12)
    request = bytearray()
    while b"\r\n\r\n" not in request and len(request) < 8192:
        piece = conn.recv(2048)
        if not piece:
            raise ConnectionError("connection ended before CONNECT")
        request.extend(piece)
    first_line = request.split(b"\r\n", 1)[0]
    # Intentionally store no headers; they could contain proxy credentials.
    if not first_line.startswith(b"CONNECT chatgpt.com:443 HTTP/"):
        raise ValueError("only CONNECT chatgpt.com:443 is accepted")
    conn.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
    header = read_exact(conn, 5)
    if header[0] != 22:
        raise ValueError("first TLS record is not handshake")
    record = read_exact(conn, int.from_bytes(header[3:5], "big"))
    if len(record) < 4:
        raise ValueError("short handshake")
    need = int.from_bytes(record[1:4], "big") + 4
    while len(record) < need:
        header = read_exact(conn, 5)
        if header[0] != 22:
            raise ValueError("unexpected TLS record")
        record += read_exact(conn, int.from_bytes(header[3:5], "big"))
    return parse_hello(record)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--bind", default="127.0.0.1")
    p.add_argument("--port", type=int, default=18865)
    p.add_argument("--timeout", type=int, default=45)
    args = p.parse_args()
    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind((args.bind, args.port))
    listener.listen(8)
    listener.settimeout(1)
    print("READY", listener.getsockname()[1], flush=True)
    end = time.monotonic() + args.timeout
    while time.monotonic() < end:
        try:
            conn, _ = listener.accept()
        except socket.timeout:
            continue
        with conn:
            try:
                hello = capture(conn)
                print(json.dumps(hello, sort_keys=True), flush=True)
                return
            except (ValueError, OSError, ConnectionError) as err:
                print("IGNORED", type(err).__name__, str(err), flush=True)
    raise TimeoutError("no ClientHello received")


if __name__ == "__main__":
    main()

import socket
import os
import sys
import json
import time

socket_path = "/tmp/token.sock"

if os.path.exists(socket_path):
    os.remove(socket_path)

server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
server.bind(socket_path)
server.listen(1)

print(f"Listening on {socket_path}...")

try:
    while True:
        conn, addr = server.accept()
        try:
            data = conn.recv(1024)
            if data:
                print(f"[{time.strftime('%Y-%m-%d %H:%M:%S')}] Received request from GCS Fuse")
                # Respond with a short-lived token (10 seconds) to force refresh
                token_data = {
                    "access_token": "DUMMY_TOKEN_FROM_SERVER",
                    "expires_in": 10,
                    "token_type": "Bearer"
                }
                response_body = json.dumps(token_data)
                response = f"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{response_body}".encode('utf-8')
                conn.sendall(response)
        finally:
            conn.close()
except KeyboardInterrupt:
    print("Shutting down.")
finally:
    os.remove(socket_path)

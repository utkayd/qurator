"""Exercise an actual image with isolated storage; requires Docker and Python 3.

Usage: python3 tests/docker/smoke.py [image] [platform]
Only the temporary container and volume created by this script are removed.
"""
import json
import http.client
import secrets
import subprocess
import sys
import time
import urllib.request
import urllib.parse


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True).strip()


image = sys.argv[1] if len(sys.argv) > 1 else "qurator:local"
platform = ["--platform", sys.argv[2]] if len(sys.argv) > 2 else []
name = "qurator-smoke-" + secrets.token_hex(6)
volume = name + "-data"
password = secrets.token_urlsafe(24)
cookie = ""


def start(bootstrap):
    env = ["-e", "QURATOR_SERVER_BASE_URL=http://localhost:8080"]
    if bootstrap:
        env += ["-e", "QURATOR_AUTH_BOOTSTRAP_EMAIL=smoke@example.com",
                "-e", "QURATOR_AUTH_BOOTSTRAP_PASSWORD=" + password]
    docker("run", "-d", "--name", name, *platform, "--read-only",
           "--health-interval=1s", "--health-start-period=1s",
           "-p", "127.0.0.1::8080", "-v", volume + ":/data", *env, image)
    for _ in range(90):
        info = json.loads(docker("inspect", name))[0]
        if info["State"]["Health"]["Status"] == "healthy":
            assert info["Config"]["User"] == "65532:65532", "unexpected runtime user"
            docker("exec", name, "/qurator", "healthcheck", "--live")
            port = info["NetworkSettings"]["Ports"]["8080/tcp"][0]["HostPort"]
            return "http://127.0.0.1:" + port
        if not info["State"]["Running"]:
            raise RuntimeError("container exited during startup")
        time.sleep(1)
    raise RuntimeError("container never became healthy")


def request(path, body=None):
    # API image URLs use the configured scan origin; reach this container through
    # its randomly allocated loopback port while preserving path and query.
    parsed = urllib.parse.urlsplit(path)
    path = parsed.path + ("?" + parsed.query if parsed.query else "")
    headers = {"Cookie": cookie, "X-Qurator-Requested-With": "docker-smoke"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(base + path, headers=headers,
                                 data=None if body is None else json.dumps(body).encode())
    return urllib.request.urlopen(req, timeout=10)


try:
    docker("volume", "create", volume)
    base = start(True)
    with request("/ui/signin") as response:
        assert b"<form" in response.read(), "embedded console is missing"
    with request("/v1/auth/signin", {"email": "smoke@example.com", "password": password}) as response:
        # Send the secure session cookie explicitly over this isolated loopback test.
        cookie = response.headers["Set-Cookie"].split(";", 1)[0]
    with request("/v1/codes", {"destination": "https://example.com/docker-smoke"}) as response:
        assert response.status == 201
        code = json.load(response)
    with request(code["image_url"]) as response:
        original_image = response.read()
        assert original_image.startswith(b"\x89PNG\r\n\x1a\n"), "QR image is not PNG"
        assert len(original_image) > 100, "empty rendered QR image"
    docker("stop", "--time", "10", name)
    docker("rm", name)
    base = start(False)
    # The original session must still work: both the user DB and signing key survived.
    with request("/v1/codes/" + code["id"]) as response:
        assert json.load(response)["destination"] == code["destination"]
    with request(code["image_url"]) as response:
        assert response.read() == original_image, "QR blob did not persist"
    connection = http.client.HTTPConnection(urllib.parse.urlsplit(base).netloc, timeout=10)
    try:
        connection.request("GET", urllib.parse.urlsplit(code["scan_url"]).path)
        response = connection.getresponse()
        assert response.status == 302, "public scan did not redirect"
        assert response.getheader("Location") == code["destination"]
        assert "no-store" in response.getheader("Cache-Control", "")
        response.read()
    finally:
        connection.close()
    print("PASS: nonroot, read-only root, health/readiness, authenticated QR creation, "
          "and database/signing-key/blob persistence across container replacement")
except Exception:
    subprocess.run(["docker", "logs", name], check=False)
    raise
finally:
    subprocess.run(["docker", "rm", "-f", name], check=False, stdout=subprocess.DEVNULL)
    subprocess.run(["docker", "volume", "rm", volume], check=False, stdout=subprocess.DEVNULL)

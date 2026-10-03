#!/usr/bin/env python3
"""Prepares the end-to-end stack from nothing.

    provision.py seed       before `docker compose up`: test media and the
                            qBittorrent password, which must exist before
                            qBittorrent first starts
    provision.py configure  after it is up: Jellyfin's setup wizard, a user,
                            a library and an API key

Writes .state/env.json for the scenario to read. Standard library only, so
it runs on a bare CI runner.
"""
import base64
import hashlib
import json
import os
import pathlib
import subprocess
import sys
import time
import urllib.error
import urllib.request

HERE = pathlib.Path(__file__).resolve().parent
STATE = HERE / ".state"

JELLYFIN = "http://127.0.0.1:18196"
JF_USER, JF_PASS = "e2e", "e2e-jellyfin"
QBIT_USER, QBIT_PASS = "admin", "e2e-qbittorrent"
TR_USER, TR_PASS = "e2e", "e2e-transmission"
CLIENT_AUTH = 'MediaBrowser Client="AutoThrottle E2E", Device="Living Room TV", DeviceId="e2e-player", Version="1.0.0"'


def log(msg):
    print(f"  provision: {msg}", flush=True)


def seed():
    for sub in ("autothrottle", "jellyfin", "media/movies", "qbittorrent/qBittorrent", "transmission"):
        (STATE / sub).mkdir(parents=True, exist_ok=True)

    movie = STATE / "media/movies/Sandbox Reel (2024).mkv"
    if not movie.exists():
        log("generating a 10-minute test video")
        subprocess.run([
            "docker", "run", "--rm", "--memory=512m",
            "-v", f"{movie.parent}:/out",
            "lscr.io/linuxserver/ffmpeg:latest",
            "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=5",
            "-t", "600", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
            f"/out/{movie.name}",
        ], check=True)

    # qBittorrent stores a PBKDF2-SHA512 hash; writing it before first start
    # gives the stack a known password instead of a random one in its log.
    conf = STATE / "qbittorrent/qBittorrent/qBittorrent.conf"
    if not conf.exists():
        salt = os.urandom(16)
        key = hashlib.pbkdf2_hmac("sha512", QBIT_PASS.encode(), salt, 100000, dklen=64)
        conf.write_text("\n".join([
            "[LegalNotice]", "Accepted=true", "",
            "[Preferences]",
            f"WebUI\\Username={QBIT_USER}",
            f'WebUI\\Password_PBKDF2="@ByteArray({base64.b64encode(salt).decode()}:{base64.b64encode(key).decode()})"',
            # AutoThrottle reaches it by service name, which host-header
            # validation would otherwise refuse.
            "WebUI\\HostHeaderValidation=false",
            "", ]))
        log("seeded the qBittorrent password")


def call(path, payload=None, method=None, token=None, base=JELLYFIN):
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(base + path, data=data, method=method or ("POST" if data is not None else "GET"))
    req.add_header("Content-Type", "application/json")
    auth = CLIENT_AUTH + (f', Token="{token}"' if token else "")
    req.add_header("Authorization", auth)
    with urllib.request.urlopen(req, timeout=20) as resp:
        body = resp.read().decode()
        return json.loads(body) if body.strip() else None


def wait_for(what, check, timeout=180):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            if check():
                return
        except (urllib.error.URLError, ConnectionError, OSError):
            pass
        time.sleep(2)
    sys.exit(f"provision: timed out waiting for {what}")


def configure():
    wait_for("Jellyfin", lambda: urllib.request.urlopen(JELLYFIN + "/health", timeout=5).read() == b"Healthy")

    log("running Jellyfin's setup wizard")
    call("/Startup/Configuration", {"UICulture": "en-US", "MetadataCountryCode": "US", "PreferredMetadataLanguage": "en"})
    call("/Startup/User")
    call("/Startup/User", {"Name": JF_USER, "Password": JF_PASS})
    call("/Startup/RemoteAccess", {"EnableRemoteAccess": True, "EnableAutomaticPortMapping": False})
    call("/Startup/Complete", {})

    session = call("/Users/AuthenticateByName", {"Username": JF_USER, "Pw": JF_PASS})
    token, user_id = session["AccessToken"], session["User"]["Id"]

    log("adding the test library")
    call("/Library/VirtualFolders?name=Movies&collectionType=movies&refreshLibrary=true",
         {"LibraryOptions": {"PathInfos": [{"Path": "/media/movies"}]}}, token=token)

    item = {}

    def scanned():
        items = call(f"/Items?Recursive=true&IncludeItemTypes=Movie&userId={user_id}", token=token)["Items"]
        if items:
            item.update(items[0])
        return bool(items)
    wait_for("the library scan", scanned)

    # The key AutoThrottle is given, made the way a person would: as an
    # app key from Jellyfin's dashboard, not the user's session token.
    call("/Auth/Keys?app=AutoThrottle", {}, token=token)
    keys = call("/Auth/Keys", token=token)["Items"]
    api_key = next(k["AccessToken"] for k in keys if k.get("AppName") == "AutoThrottle")

    env = {
        "jellyfin": {"base": JELLYFIN, "internal": "http://jellyfin:8096", "apiKey": api_key,
                     "userToken": token, "itemId": item["Id"], "itemName": item["Name"],
                     "runTicks": item.get("RunTimeTicks", 6_000_000_000)},
        "qbittorrent": {"base": "http://127.0.0.1:18180", "internal": "http://qbittorrent:8080",
                        "username": QBIT_USER, "password": QBIT_PASS},
        "transmission": {"base": "http://127.0.0.1:18191", "internal": "http://transmission:9091",
                         "username": TR_USER, "password": TR_PASS},
        "autothrottle": "http://127.0.0.1:17676",
    }
    (STATE / "env.json").write_text(json.dumps(env, indent=2))
    log(f"ready: {item['Name']}, API key ...{api_key[-4:]}")


if __name__ == "__main__":
    {"seed": seed, "configure": configure}[sys.argv[1]]()

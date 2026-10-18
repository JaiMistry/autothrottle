#!/usr/bin/env python3
"""Prepares the end-to-end stack from nothing.

    provision.py seed       before `docker compose up`: the qBittorrent
                            password, which must exist before qBittorrent
                            first starts
    provision.py claim      Plex only: prints a one-time code that claims
                            the new server for the test account
    provision.py configure  after it is up: the media server's setup
                            (MEDIA=jellyfin, emby or plex), a library, and
                            for Jellyfin and Emby a user and an API key
    provision.py cleanup    Plex only: removes the run's server from the
                            test account, so the account does not fill up

Writes .state/env.json for the scenario to read. The test video is
committed (fixtures/), not made here. Standard library only, so it runs
on a bare CI runner.

Plex needs the test account's token in PLEX_E2E_TOKEN, and its client
identifier in PLEX_E2E_CLIENT_ID. Neither is ever written to disk here.
"""
import base64
import hashlib
import http.client
import json
import os
import pathlib
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET

HERE = pathlib.Path(__file__).resolve().parent
STATE = HERE / ".state"

MEDIA = os.environ.get("MEDIA", "jellyfin")
SERVERS = {
    "jellyfin": {"label": "Jellyfin", "base": "http://127.0.0.1:18196", "internal": "http://jellyfin:8096"},
    "emby": {"label": "Emby", "base": "http://127.0.0.1:18096", "internal": "http://emby:8096"},
    "plex": {"label": "Plex", "base": "http://127.0.0.1:18324", "internal": "http://plex:32400"},
}
SERVER = SERVERS[MEDIA]
MS_USER, MS_PASS = "e2e", "e2e-media"
QBIT_USER, QBIT_PASS = "admin", "e2e-qbittorrent"
TR_USER, TR_PASS = "e2e", "e2e-transmission"
DELUGE_PASS = "deluge"  # the image's default; the Web UI has no username
CLIENT_AUTH = 'MediaBrowser Client="AutoThrottle E2E", Device="Living Room TV", DeviceId="e2e-player", Version="1.0.0"'


def log(msg):
    print(f"  provision: {msg}", flush=True)


def seed():
    for sub in ("autothrottle", MEDIA, "qbittorrent/qBittorrent", "transmission", "deluge"):
        (STATE / sub).mkdir(parents=True, exist_ok=True)

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


def call(path, payload=None, method=None, token=None, base=SERVER["base"]):
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(base + path, data=data, method=method or ("POST" if data is not None else "GET"))
    req.add_header("Content-Type", "application/json")
    auth = CLIENT_AUTH + (f', Token="{token}"' if token else "")
    # Jellyfin reads Authorization; Emby reads its own header.
    req.add_header("Authorization", auth)
    req.add_header("X-Emby-Authorization", auth)
    if token:
        req.add_header("X-Emby-Token", token)
    # A server reporting ready can still answer 503, or drop the connection,
    # while it finishes starting. The wizard's calls are safe to repeat.
    for attempt in range(30):
        try:
            with urllib.request.urlopen(req, timeout=20) as resp:
                body = resp.read().decode()
                return json.loads(body) if body.strip() else None
        except urllib.error.HTTPError as e:
            if e.code != 503 or attempt == 29:
                raise
            time.sleep(2)
        # A reset, broken pipe or close mid-reply; urllib wraps some in URLError.
        except (urllib.error.URLError, ConnectionError, http.client.HTTPException):
            if attempt == 29:
                raise
            time.sleep(2)


def wait_for(what, check, timeout=180):
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        try:
            if check():
                return
        except (urllib.error.URLError, ConnectionError, OSError) as e:
            last = e
        time.sleep(2)
    sys.exit(f"provision: timed out waiting for {what}" + (f" (last error: {last})" if last else ""))


# --- Plex ---------------------------------------------------------------------

def plex_headers():
    return {
        "Accept": "application/json",
        "X-Plex-Token": os.environ["PLEX_E2E_TOKEN"],
        "X-Plex-Client-Identifier": os.environ["PLEX_E2E_CLIENT_ID"],
        "X-Plex-Product": "AutoThrottle E2E",
        "X-Plex-Version": "1.0",
    }


def plex(url, method="GET", raw=False):
    req = urllib.request.Request(url, method=method, headers=plex_headers())
    with urllib.request.urlopen(req, timeout=30) as resp:
        body = resp.read()
        return body if raw else (json.loads(body) if body.strip() else None)


def claim():
    # Printed for reset.sh to hand to the container; valid for minutes.
    print(plex("https://plex.tv/api/claim/token.json")["token"])


def configure_plex():
    base = SERVER["base"]
    identity = {}

    def claimed():
        with urllib.request.urlopen(base + "/identity", timeout=5) as resp:
            root = ET.fromstring(resp.read())
        identity.update(root.attrib)
        return root.get("claimed") == "1"
    wait_for("Plex to start and be claimed", claimed)
    (STATE / "plex-machine-id").write_text(identity["machineIdentifier"])

    log("adding the test library")
    q = urllib.parse.urlencode({"name": "Movies", "type": "movie", "agent": "tv.plex.agents.movie",
                                "scanner": "Plex Movie", "language": "en-US", "location": "/media/movies"})

    def created():
        try:
            plex(f"{base}/library/sections?{q}", "POST")
            return True
        except urllib.error.HTTPError as e:
            if e.code in (400, 503):  # still starting its plug-ins
                return False
            raise
    wait_for("Plex to accept the library", created)

    item = {}

    def scanned():
        section = plex(f"{base}/library/sections")["MediaContainer"]["Directory"][0]["key"]
        found = plex(f"{base}/library/sections/{section}/all")["MediaContainer"].get("Metadata", [])
        if found:
            item.update(found[0])
        return bool(found)
    wait_for("the library scan", scanned)

    # Matching and analysing the new item keeps Plex busy, and slow to
    # answer, for a while after it appears.
    def idle():
        return not plex(f"{base}/activities")["MediaContainer"].get("Activity")
    wait_for("Plex to finish with the library", idle)

    write_env({"kind": "plex", "label": SERVER["label"], "base": base, "internal": SERVER["internal"],
               "itemId": item["ratingKey"], "itemName": item["title"], "durationMs": int(item.get("duration", 600000))})
    log(f"ready: {item['title']}, server {identity['machineIdentifier'][:8]}…")


def cleanup():
    """Removes this run's server, and the AutoThrottle sign-ins it made,
    from the test account. Another run can be using the account at the
    same time (the newest round runs beside the pinned one), so what a
    crashed run left behind goes only once it is older than any run can
    be. Only things E2E makes: the account is the tests' own."""
    ours = set()
    for name in ("plex-machine-id", "plex-signins"):
        f = STATE / name
        if f.exists():
            ours.update(f.read_text().split())
    # plex.tv barely updates lastSeenAt, so age is counted from creation;
    # a job is stopped after 30 minutes.
    stale = time.time() - 3600
    root = ET.fromstring(plex("https://plex.tv/devices.xml", raw=True))
    removed = 0
    for d in root.findall("Device"):
        if d.get("clientIdentifier") == os.environ["PLEX_E2E_CLIENT_ID"]:
            continue
        # Servers are named e2e-plex-<run> (compose.yml's hostname).
        made_here = (d.get("name") or "").startswith("e2e-plex") or d.get("product") == "AutoThrottle"
        left_behind = made_here and int(d.get("createdAt") or 0) < stale
        if d.get("clientIdentifier") in ours or left_behind:
            plex(f"https://plex.tv/devices/{d.get('id')}.xml", "DELETE", raw=True)
            removed += 1
    log(f"removed {removed} test device(s) from the Plex account")


def write_env(media):
    env = {
        "media": media,
        "qbittorrent": {"base": "http://127.0.0.1:18180", "internal": "http://qbittorrent:8080",
                        "username": QBIT_USER, "password": QBIT_PASS},
        "transmission": {"base": "http://127.0.0.1:18191", "internal": "http://transmission:9091",
                         "username": TR_USER, "password": TR_PASS},
        "deluge": {"base": "http://127.0.0.1:18112", "internal": "http://deluge:8112",
                   "username": "", "password": DELUGE_PASS},
        "autothrottle": "http://127.0.0.1:17676",
    }
    (STATE / "env.json").write_text(json.dumps(env, indent=2))


# --- Jellyfin and Emby ---------------------------------------------------------

def configure():
    if MEDIA == "plex":
        configure_plex()
        return
    # Jellyfin answers some requests while still starting up; /health is
    # its own "ready" signal. Emby has none, so its public info stands in.
    ready = "/health" if MEDIA == "jellyfin" else "/System/Info/Public"

    def up():
        with urllib.request.urlopen(SERVER["base"] + ready, timeout=5) as resp:
            return resp.status == 200
    wait_for(SERVER["label"], up)

    log(f"running {SERVER['label']}'s setup wizard")
    call("/Startup/Configuration", {"UICulture": "en-US", "MetadataCountryCode": "US", "PreferredMetadataLanguage": "en"})
    call("/Startup/User")
    call("/Startup/User", {"Name": MS_USER, "Password": MS_PASS})
    if MEDIA == "jellyfin":
        call("/Startup/RemoteAccess", {"EnableRemoteAccess": True, "EnableAutomaticPortMapping": False})
    call("/Startup/Complete", {})

    session = call("/Users/AuthenticateByName", {"Username": MS_USER, "Pw": MS_PASS})
    token, user_id = session["AccessToken"], session["User"]["Id"]

    log("adding the test library")
    call("/Library/VirtualFolders?name=Movies&collectionType=movies&refreshLibrary=true",
         {"LibraryOptions": {"PathInfos": [{"Path": "/media/movies"}]}}, token=token)

    item = {}
    items_path = (f"/Items?Recursive=true&IncludeItemTypes=Movie&userId={user_id}" if MEDIA == "jellyfin"
                  else f"/Users/{user_id}/Items?Recursive=true&IncludeItemTypes=Movie")

    asked = [time.time()]

    def scanned():
        items = call(items_path, token=token)["Items"]
        if items:
            item.update(items[0])
            return True
        # Now and then Jellyfin 12 adds the library without scanning it.
        # Asking again is what a person would do.
        if time.time() - asked[0] > 30:
            asked[0] = time.time()
            log("no scan yet; asking for one")
            call("/Library/Refresh", method="POST", token=token)
        return False
    wait_for("the library scan", scanned)

    # An app key from the dashboard, as a person would make it, not the
    # user's session token.
    call("/Auth/Keys?app=AutoThrottle", {}, token=token)
    keys = call("/Auth/Keys", token=token)["Items"]
    api_key = next(k["AccessToken"] for k in keys if k.get("AppName") == "AutoThrottle")

    write_env({"kind": MEDIA, "label": SERVER["label"], "base": SERVER["base"], "internal": SERVER["internal"],
               "apiKey": api_key, "userToken": token, "itemId": item["Id"], "itemName": item["Name"],
               "runTicks": item.get("RunTimeTicks", 6_000_000_000)})
    log(f"ready: {item['Name']}, API key ...{api_key[-4:]}")


if __name__ == "__main__":
    {"seed": seed, "claim": claim, "configure": configure, "cleanup": cleanup}[sys.argv[1]]()

#!/usr/bin/env python3
"""Instagram bridge for the Go panel.

A small local HTTP service the panel starts and calls to talk to Instagram
as a logged-in account (username + password), using the instagrapi
library. Each panel account gets its own instagrapi Client with its own
proxy (IP) and a persisted session (device ids + cookies) that the panel
stores encrypted, so an account keeps looking like the same phone on the
same IP across restarts and never has to log in again unless Instagram
ends the session.

Listens on 127.0.0.1 only and requires the token the panel passes in the
BRIDGE_TOKEN environment variable. Passwords are never logged.
"""

import argparse
import hmac
import io
import json
import logging
import os
import sys
import tempfile
import threading
import time
import urllib.request
import uuid
import warnings
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

warnings.filterwarnings("ignore")

from instagrapi import Client  # noqa: E402

log = logging.getLogger("bridge")

TOKEN = os.environ.get("BRIDGE_TOKEN", "")
LOGIN_WAIT_SECONDS = 120
CODE_WAIT_SECONDS = 600
FLOW_TTL_SECONDS = 15 * 60
MAX_DOWNLOAD_BYTES = 200 * 1024 * 1024


# ---------- errors ----------

CHALLENGE_ERRORS = {
    "ChallengeRequired", "ChallengeError", "ChallengeUnknownStep", "ChallengeRedirection",
    "SelectContactPointRecoveryForm", "RecaptchaChallengeForm", "SubmitPhoneNumberForm",
    "LegacyForceSetNewPasswordForm",
}


class BridgeError(Exception):
    def __init__(self, kind, message):
        super().__init__(message)
        self.kind = kind
        self.message = message


def classify(exc):
    """Map an instagrapi/requests exception to (kind, Turkish message)."""
    if isinstance(exc, BridgeError):
        return exc.kind, exc.message
    name = type(exc).__name__
    detail = str(exc).strip() or name
    if name in ("BadPassword", "BadCredentials"):
        return "bad_password", "Kullanıcı adı veya şifre hatalı."
    if name in CHALLENGE_ERRORS:
        return "challenge", ("Instagram bu giriş için güvenlik doğrulaması istiyor. Hesaba telefondaki "
                             "Instagram uygulamasından girip \"Bu bendim\" onayını verin, sonra tekrar deneyin.")
    if name in ("LoginRequired", "ClientLoginRequired", "ReloginAttemptExceeded"):
        return "login_required", "Instagram oturumu sona erdi; yeniden giriş gerekiyor."
    if name in ("PleaseWaitFewMinutes", "RateLimitError", "ClientThrottledError"):
        return "rate_limited", "Instagram çok fazla istek algıladı; birkaç dakika bekleyip tekrar deneyin."
    if name == "FeedbackRequired":
        return "feedback", "Instagram bu işlemi geçici olarak engelledi: " + detail
    if name in ("ProxyAddressIsBlocked", "SentryBlock"):
        return "ip_blocked", "Instagram bu IP adresini engellemiş; hesaba farklı bir IP (proxy) atayın."
    if name in ("ConnectionError", "ProxyError", "ConnectTimeout", "ReadTimeout", "ClientConnectionError",
                "ClientRequestTimeout", "SSLError", "Timeout", "MaxRetryError", "NewConnectionError"):
        log.info("network error: %s", detail)
        return "network", "Instagram'a bağlanılamadı; hesaba atanan IP/proxy çalışmıyor olabilir."
    return "other", detail


# ---------- clients ----------

def maybe(client, method, *args):
    fn = getattr(client, method, None)
    if fn:
        try:
            fn(*args)
        except Exception:  # cosmetic device settings must never break a login
            pass


def new_client(proxy=None, settings=None):
    cl = Client()
    cl.logger.setLevel(logging.WARNING)
    if settings:
        cl.set_settings(settings)
    else:
        # A Turkish brand account should look like a phone in Turkey.
        maybe(cl, "set_locale", "tr_TR")
        maybe(cl, "set_country", "TR")
        maybe(cl, "set_country_code", 90)
        maybe(cl, "set_timezone_offset", 3 * 3600)
    cl.set_proxy(proxy or None)
    cl.delay_range = [1, 3]
    return cl


class AccountClients:
    """One cached Client per panel account, rebuilt when the panel sends a
    different proxy or session than the one the cached client holds.
    instagrapi clients are not thread-safe, so calls per account are
    serialized."""

    def __init__(self):
        self.lock = threading.Lock()
        self.entries = {}

    def _entry(self, account_id):
        with self.lock:
            entry = self.entries.get(account_id)
            if entry is None:
                entry = {"lock": threading.Lock(), "client": None, "proxy": None, "fingerprint": None}
                self.entries[account_id] = entry
            return entry

    def run(self, req, fn):
        account_id = req.get("account_id")
        settings = req.get("settings") or None
        if not settings:
            raise BridgeError("login_required", "Bu hesabın kayıtlı oturumu yok; yeniden giriş yapın.")
        proxy = req.get("proxy") or None
        fingerprint = json.dumps(settings, sort_keys=True)
        entry = self._entry(account_id)
        with entry["lock"]:
            if entry["client"] is None or entry["proxy"] != proxy or entry["fingerprint"] != fingerprint:
                entry["client"] = new_client(proxy, settings)
                entry["proxy"] = proxy
            cl = entry["client"]
            try:
                result = fn(cl)
            finally:
                new_settings = cl.get_settings()
                entry["fingerprint"] = json.dumps(new_settings, sort_keys=True)
            result["settings"] = new_settings
            return result

    def forget(self, account_id):
        with self.lock:
            self.entries.pop(account_id, None)


CLIENTS = AccountClients()


# ---------- login flows ----------

class LoginFlow:
    """One username/password login. It runs in a background thread so that
    when Instagram asks for a code (2FA or a security challenge) the HTTP
    request can return, and a later request can hand the code in."""

    def __init__(self, username, password, proxy, settings):
        self.id = uuid.uuid4().hex
        self.username = username
        self.password = password
        self.created = time.time()
        self.cond = threading.Condition()
        self.state = "running"  # running | need_code | done | error
        self.kind = None        # two_factor | challenge
        self.message = ""
        self.code = None
        self.result = None
        self.error = None
        self.two_factor_info = None

        self.client = new_client(proxy)
        if settings and settings.get("uuids"):
            # Re-login keeps the same "phone" (instagrapi best practice).
            self.client.set_uuids(settings["uuids"])
        self.client.challenge_code_handler = self._challenge_code
        self.client.change_password_handler = lambda username: None

    # Called by instagrapi (inside the login thread) when Instagram sends a
    # security code by SMS or e-mail.
    def _challenge_code(self, username, choice):
        via = "e-posta adresinize" if "EMAIL" in str(choice).upper() else "telefonunuza"
        with self.cond:
            self.state, self.kind = "need_code", "challenge"
            self.message = f"Instagram {via} bir güvenlik kodu gönderdi. Kodu girin."
            self.cond.notify_all()
            self.cond.wait_for(lambda: self.code is not None, timeout=CODE_WAIT_SECONDS)
            code, self.code = self.code, None
            self.state = "running"
            return code or False

    def start(self):
        self._spawn(lambda: self.client.login(self.username, self.password))

    def _spawn(self, fn):
        threading.Thread(target=self._run, args=(fn,), daemon=True).start()

    def _run(self, fn):
        try:
            fn()
            result = {"settings": self.client.get_settings(), "user": user_info(self.client)}
            with self.cond:
                self.state, self.result = "done", result
                self.cond.notify_all()
        except Exception as exc:  # noqa: BLE001 - every failure is reported to the panel
            with self.cond:
                if type(exc).__name__ == "TwoFactorRequired" and self.two_factor_info is None:
                    info = (self.client.last_json or {}).get("two_factor_info") or {}
                    self.two_factor_info = info
                    self.state, self.kind = "need_code", "two_factor"
                    if info.get("totp_two_factor_on"):
                        self.message = "Hesapta iki adımlı doğrulama açık. Authenticator uygulamasındaki kodu girin."
                    else:
                        phone = info.get("obfuscated_phone_number") or ""
                        self.message = f"Hesapta iki adımlı doğrulama açık. Telefonunuza (…{phone}) gelen SMS kodunu girin."
                else:
                    self.state, self.error = "error", exc
                self.cond.notify_all()

    def _two_factor_login(self, code):
        # Same request instagrapi makes, but answering the 2FA prompt of the
        # first attempt (no second login request, so no second SMS) and with
        # the method that matches how the account receives codes.
        cl, info = self.client, self.two_factor_info or {}
        data = {
            "verification_code": code,
            "phone_id": cl.phone_id,
            "_csrftoken": cl.token,
            "two_factor_identifier": info.get("two_factor_identifier"),
            "username": self.username,
            "trust_this_device": "1",
            "guid": cl.uuid,
            "device_id": cl.android_device_id,
            "waterfall_id": str(uuid.uuid4()),
            "verification_method": "3" if info.get("totp_two_factor_on") else "1",
        }
        cl.private_request("accounts/two_factor_login/", data, login=True)
        cl.authorization_data = cl.parse_authorization(cl.last_response.headers.get("ig-set-authorization"))
        cl.login_flow()
        cl.last_login = time.time()

    def submit_code(self, code):
        with self.cond:
            if self.state != "need_code":
                raise BridgeError("other", "Bu giriş şu an kod beklemiyor.")
            if self.kind == "challenge":
                self.code = code
                self.state = "running"
                self.cond.notify_all()
                return
            self.state = "running"
        self._spawn(lambda: self._two_factor_login(code))

    def wait(self):
        with self.cond:
            self.cond.wait_for(lambda: self.state != "running", timeout=LOGIN_WAIT_SECONDS)
            if self.state == "running":
                raise BridgeError("network", "Instagram zamanında yanıt vermedi; tekrar deneyin.")
            if self.state == "need_code":
                return {"status": "code_required", "login_id": self.id, "kind": self.kind, "message": self.message}
            if self.state == "error":
                raise self.error
            return dict(self.result, status="ok", login_id=self.id)


FLOWS = {}
FLOWS_LOCK = threading.Lock()


def prune_flows():
    with FLOWS_LOCK:
        for key in [k for k, f in FLOWS.items() if time.time() - f.created > FLOW_TTL_SECONDS]:
            del FLOWS[key]


# ---------- mapping ----------

def s(value):
    return "" if value is None else str(value)


def user_info(cl):
    try:
        u = cl.user_info_v1(str(cl.user_id))
    except Exception:  # noqa: BLE001 - login already succeeded; details are optional
        return {"pk": s(cl.user_id), "username": cl.username or ""}
    return {
        "pk": s(u.pk),
        "username": u.username or "",
        "full_name": u.full_name or "",
        "profile_pic_url": s(getattr(u, "profile_pic_url_hd", None) or u.profile_pic_url),
        "follower_count": u.follower_count or 0,
        "following_count": u.following_count or 0,
        "media_count": u.media_count or 0,
        "account_type": "BUSINESS" if getattr(u, "is_business", False) else "PERSONAL",
    }


MEDIA_TYPES = {1: "IMAGE", 2: "VIDEO", 8: "CAROUSEL_ALBUM"}


def media_dict(m):
    thumb = s(m.thumbnail_url)
    if not thumb and getattr(m, "resources", None):
        thumb = s(m.resources[0].thumbnail_url)
    return {
        "id": s(m.id),
        "caption": m.caption_text or "",
        "media_type": MEDIA_TYPES.get(m.media_type, s(m.media_type)),
        "media_url": thumb,
        "thumbnail_url": thumb,
        "permalink": f"https://www.instagram.com/p/{m.code}/" if m.code else "",
        "timestamp": m.taken_at.isoformat() if m.taken_at else "",
        "like_count": m.like_count or 0,
        "comments_count": m.comment_count or 0,
    }


def comment_dict(c):
    return {
        "id": s(c.pk),
        "text": c.text or "",
        "username": c.user.username if c.user else "",
        "timestamp": c.created_at_utc.isoformat() if c.created_at_utc else "",
        "like_count": c.like_count or 0,
    }


# ---------- media download for publishing ----------

def download(url):
    req = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0"})
    with urllib.request.urlopen(req, timeout=60) as resp:
        data = resp.read(MAX_DOWNLOAD_BYTES + 1)
    if len(data) > MAX_DOWNLOAD_BYTES:
        raise BridgeError("other", "Medya dosyası çok büyük (en fazla 200 MB).")
    return data


def save_temp(data, media_type):
    """Write media to a temp file. Images become upright JPEGs, which is
    what Instagram accepts (PNG/WebP/phone photos with EXIF rotation)."""
    fd, path = tempfile.mkstemp(suffix=".jpg" if media_type == "IMAGE" else ".mp4")
    with os.fdopen(fd, "wb") as f:
        if media_type == "IMAGE":
            from PIL import Image, ImageOps
            img = ImageOps.exif_transpose(Image.open(io.BytesIO(data)))
            img.convert("RGB").save(f, "JPEG", quality=95)
        else:
            f.write(data)
    return path


def media_file(req, media_type):
    """Return (path, is_temp) for the media to publish: a file uploaded in
    the panel (req["path"]) or a URL to download."""
    src = req.get("path")
    if src:
        if not os.path.isfile(src):
            raise BridgeError("other", "Paylaşılacak dosya bulunamadı.")
        if media_type == "IMAGE":
            with open(src, "rb") as f:
                return save_temp(f.read(), media_type), True
        return src, False
    return save_temp(download(req.get("url", "")), media_type), True


def publish(cl, req):
    media_type = req.get("media_type")
    if media_type not in ("IMAGE", "REELS"):
        raise BridgeError("other", "media_type IMAGE veya REELS olmalı")
    if media_type == "REELS":
        try:
            import moviepy  # noqa: F401
        except ImportError:
            raise BridgeError("other", "Reels paylaşmak için köprüye 'moviepy' kurulmalı: "
                                       "bridge/.venv/bin/pip install moviepy==1.0.3")
    path, is_temp = media_file(req, media_type)
    try:
        if media_type == "IMAGE":
            media = cl.photo_upload(path, req.get("caption", ""))
        else:
            media = cl.clip_upload(path, req.get("caption", ""))
    finally:
        if is_temp:
            try:
                os.remove(path)
            except OSError:
                pass
    return {"id": s(media.id), "code": s(media.code)}


# ---------- routes ----------

def route_login(req):
    prune_flows()
    username = (req.get("username") or "").strip().lstrip("@")
    password = req.get("password") or ""
    if not username or not password:
        raise BridgeError("bad_password", "Kullanıcı adı ve şifre gerekli.")
    flow = LoginFlow(username, password, req.get("proxy") or None, req.get("settings") or None)
    with FLOWS_LOCK:
        FLOWS[flow.id] = flow
    flow.start()
    return flow.wait()


def route_login_code(req):
    with FLOWS_LOCK:
        flow = FLOWS.get(req.get("login_id", ""))
    if flow is None:
        raise BridgeError("other", "Giriş oturumu bulunamadı veya süresi doldu; baştan deneyin.")
    code = "".join(ch for ch in s(req.get("code")) if ch.isdigit())
    if not code:
        raise BridgeError("other", "Doğrulama kodunu girin.")
    flow.submit_code(code)
    return flow.wait()


def int_ids(ids):
    return [int(i) for i in ids]


ROUTES = {
    "/login": route_login,
    "/login/code": route_login_code,
    "/account/info": lambda req: CLIENTS.run(req, lambda cl: {"user": user_info(cl)}),
    "/account/medias": lambda req: CLIENTS.run(req, lambda cl: {
        "media": [media_dict(m) for m in cl.user_medias_v1(str(cl.user_id), int(req.get("amount") or 24))]}),
    "/account/comments": lambda req: CLIENTS.run(req, lambda cl: {
        "comments": [comment_dict(c) for c in cl.media_comments(req["media_id"], int(req.get("amount") or 500))]}),
    "/account/comment/delete": lambda req: CLIENTS.run(req, lambda cl: {
        "deleted": cl.comment_bulk_delete(req["media_id"], int_ids([req["comment_id"]]))}),
    "/account/comment/reply": lambda req: CLIENTS.run(req, lambda cl: {
        "id": s(cl.media_comment(req["media_id"], req["text"], replied_to_comment_id=int(req["comment_id"])).pk)}),
    "/account/publish": lambda req: CLIENTS.run(req, lambda cl: publish(cl, req)),
    "/account/forget": lambda req: (CLIENTS.forget(req.get("account_id")), {})[1],
}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):  # request lines would include nothing secret, but keep output quiet
        pass

    def _send(self, status, payload):
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _authorized(self):
        return bool(TOKEN) and hmac.compare_digest(self.headers.get("X-Bridge-Token", ""), TOKEN)

    def do_GET(self):
        if self.path == "/health" and self._authorized():
            return self._send(200, {"ok": True})
        self._send(404, {"ok": False, "error": {"kind": "other", "message": "not found"}})

    def do_POST(self):
        if not self._authorized():
            return self._send(401, {"ok": False, "error": {"kind": "other", "message": "unauthorized"}})
        route = ROUTES.get(self.path)
        if route is None:
            return self._send(404, {"ok": False, "error": {"kind": "other", "message": "not found"}})
        try:
            length = int(self.headers.get("Content-Length") or 0)
            req = json.loads(self.rfile.read(length) or b"{}")
            result = route(req)
            self._send(200, dict(result, ok=True))
        except Exception as exc:  # noqa: BLE001 - errors are returned to the panel, not raised
            kind, message = classify(exc)
            if kind == "other":
                log.warning("%s failed: %s: %s", self.path, type(exc).__name__, exc)
            self._send(200, {"ok": False, "error": {"kind": kind, "message": message}})


class Server(ThreadingHTTPServer):
    daemon_threads = True

    def handle_error(self, request, client_address):
        # The panel closing an idle keep-alive connection is not an error.
        if isinstance(sys.exc_info()[1], (ConnectionResetError, BrokenPipeError)):
            return
        super().handle_error(request, client_address)


def exit_with_parent():
    """Stop when the panel process that started us goes away."""
    parent = os.getppid()
    while True:
        time.sleep(2)
        if os.getppid() != parent:
            os._exit(0)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=18091)
    parser.add_argument("--standalone", action="store_true", help="do not exit when the parent process exits")
    args = parser.parse_args()
    logging.basicConfig(level=logging.INFO, format="[bridge] %(message)s", stream=sys.stderr)
    if not TOKEN:
        sys.exit("BRIDGE_TOKEN is required")
    if not args.standalone:
        threading.Thread(target=exit_with_parent, daemon=True).start()
    server = Server((args.host, args.port), Handler)
    log.info("listening on %s:%d", args.host, args.port)
    server.serve_forever()


if __name__ == "__main__":
    main()

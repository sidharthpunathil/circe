#!/usr/bin/env python3
"""End-to-end tests: a real browser against a real server.

The Go tests cover the store, the handlers and the sync in isolation. This
covers the thing none of them can: that a person can open the site and get
through the work. It builds the server, starts it on a scratch data directory,
and drives Chromium through the flows that matter.

    make e2e            # or: python3 e2e/e2e.py

Needs Playwright (pip install playwright && playwright install chromium). It is
a test dependency only: nothing here is needed to build or run Circe, which has
no Node or Python in its build.
"""

import contextlib
import os
import re
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

try:
    from playwright.sync_api import Error as PlaywrightError
    from playwright.sync_api import sync_playwright
except ImportError:
    sys.exit(
        "Playwright is not installed.\n"
        "  pip install playwright && playwright install chromium"
    )

PASSCODE = "e2e-passcode"
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


# ---------- harness ----------


def free_port():
    with contextlib.closing(socket.socket()) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def build_server(out):
    print("building circe-server ...", flush=True)
    subprocess.run(
        ["go", "build", "-o", out, "./cmd/server"],
        cwd=ROOT,
        check=True,
    )


def wait_until_up(base, timeout=30):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(base + "/api/v1/health", timeout=2) as r:
                if r.status == 200:
                    return
        except (urllib.error.URLError, urllib.error.HTTPError, OSError):
            time.sleep(0.2)
    raise RuntimeError("server did not come up at " + base)


class Results:
    """A running tally, so one failure does not hide the rest."""

    def __init__(self):
        self.passed = []
        self.failed = []

    def check(self, name, fn):
        try:
            fn()
        except Exception as err:  # noqa: BLE001 - a failed check is any exception
            self.failed.append((name, err))
            print(f"  FAIL  {name}\n        {err}", flush=True)
        else:
            self.passed.append(name)
            print(f"  ok    {name}", flush=True)

    def report(self):
        print(f"\n{len(self.passed)} passed, {len(self.failed)} failed")
        for name, err in self.failed:
            print(f"\nFAILED {name}\n{err}")
        return 1 if self.failed else 0


def path_of(url):
    """The path only.

    Substring checks on a URL are a trap here: PocketBase's dashboard is a
    single-page app that routes to /_/#/login when it has no session of its
    own, and "/login" appears in that string without the browser having gone
    anywhere near the registry's sign-in page.
    """
    return urllib.parse.urlparse(url).path


def assert_that(condition, message):
    if not condition:
        raise AssertionError(message)


def fill_form(page, action, fields):
    """Fill fields inside one form, by field name.

    Scoped because the same name appears more than once on a page (the header
    search box is also called q, and description is an input on some forms and a
    textarea on others), so a page-wide selector is a coin toss.
    """
    form = page.locator(f'form[action="{action}"]')
    for name, value in fields.items():
        form.locator(f'[name="{name}"]').first.fill(value)


def submit(page, action):
    """Submit the form with this action, and wait for the page it leads to.

    Every page carries a Sign out button in its header, and it is the first
    submit button in the document, so an unscoped click signs you out instead
    of saving your work. Scoping to the form is not a nicety here.
    """
    button = page.locator(f'form[action="{action}"] button[type="submit"]')
    assert_that(button.count() > 0, f"no submit button in the form posting to {action}")
    button.first.click()
    page.wait_for_load_state("networkidle")


# ---------- the flows ----------


def sign_in(page, base):
    page.goto(base + "/skills")
    assert_that(path_of(page.url) == "/login", f"a passworded registry must ask to sign in, landed on {page.url}")
    page.fill('input[name="passcode"]', PASSCODE)
    submit(page, "/login")
    assert_that(path_of(page.url) != "/login", f"sign-in did not go through, still at {page.url}")


def wrong_passcode_is_refused(page, base):
    ctx = page.context.browser.new_context()
    other = ctx.new_page()
    try:
        other.goto(base + "/login")
        other.fill('input[name="passcode"]', "not-the-passcode")
        submit(other, "/login")
        assert_that(path_of(other.url) == "/login", "a wrong passcode must not sign anyone in")
        other.goto(base + "/skills")
        assert_that(path_of(other.url) == "/login", "an unauthenticated browser must not reach /skills")
    finally:
        ctx.close()


def create_skill(page, base):
    page.goto(base + "/skills/new")
    fill_form(page, "/skills/new", {
        "name": "browser-made",
        "tags": "e2e, browser",
        "description": "A skill created through the browser",
        "body": "# Browser made\n\nStep one.\nStep two.\n",
    })
    submit(page, "/skills/new")

    assert_that(
        "/skills/browser-made" in page.url,
        f"after creating a skill we should be on its page, got {page.url}",
    )
    body = page.content()
    assert_that("A skill created through the browser" in body, "the description is not on the page")
    assert_that("Step one." in body, "the body is not rendered")


def skill_is_listed_and_searchable(page, base):
    page.goto(base + "/skills")
    assert_that("browser-made" in page.content(), "the new skill is not in the listing")

    page.goto(base + "/skills?q=browser")
    assert_that("browser-made" in page.content(), "search did not find the new skill")

    page.goto(base + "/skills?q=nothingmatchesthis")
    assert_that("browser-made" not in page.content(), "search returned a skill it should not have")


def edit_skill(page, base):
    page.goto(base + "/skills/browser-made/edit")
    fill_form(page, "/skills/browser-made/edit", {"description": "Edited through the browser"})
    submit(page, "/skills/browser-made/edit")
    assert_that("Edited through the browser" in page.content(), "the edit did not stick")


def add_companion_file(page, base):
    page.goto(base + "/skills/browser-made/files/new")
    fill_form(page, "/skills/browser-made/files/new", {
        "path": "references/api.md",
        "content": "# Reference\n\nDetail.\n",
    })
    submit(page, "/skills/browser-made/files/new")

    page.goto(base + "/skills/browser-made")
    assert_that("references/api.md" in page.content(), "the companion file is not listed on the skill")


def create_rule(page, base):
    page.goto(base + "/rules/new")
    fill_form(page, "/rules/new", {
        "name": "browser-rule",
        "description": "How we work, set in the browser",
        "body": "Never write an em dash.\n",
    })
    submit(page, "/rules/new")

    page.goto(base + "/rules")
    assert_that("browser-rule" in page.content(), "the new rule is not in the listing")


def create_server(page, base):
    page.goto(base + "/mcp/new")
    fill_form(page, "/mcp/new", {
        "name": "browser-server",
        "description": "A server added in the browser",
        "command": "npx",
        "args": "-y some-mcp-server",
    })
    submit(page, "/mcp/new")

    page.goto(base + "/mcp")
    assert_that("browser-server" in page.content(), "the new server is not in the listing")


def create_folder_with_all_three(page, base):
    page.goto(base + "/bundles/new")
    fill_form(page, "/bundles/new", {
        "name": "browser-folder",
        "title": "Browser Folder",
        "description": "Everything made in the browser",
    })

    for value in ("browser-made", "browser-rule", "browser-server"):
        box = page.locator(f'input[type="checkbox"][value="{value}"]')
        if box.count():
            box.first.check()

    submit(page, "/bundles/new")

    page.goto(base + "/bundles/browser-folder")
    body = page.content()
    for name in ("browser-made", "browser-rule", "browser-server"):
        assert_that(name in body, f"the folder page does not show {name}")
    assert_that("circe browser-folder" in body, "the folder page does not show the install command")


def folder_serves_a_marketplace_manifest(page, base):
    page.goto(base + "/.claude-plugin/marketplace.json")
    assert_that("browser-folder" in page.content(), "the marketplace manifest does not list the folder")


def docs_page_is_pointed_at_this_registry(page, base):
    """The Docs tab is only worth having if its commands are runnable, which
    means this registry's own URL and a folder that actually exists here."""
    page.goto(base + "/docs")
    page.wait_for_load_state("networkidle")
    body = page.content()

    assert_that("circe login " + base in body, "the docs do not show this registry's login command")
    assert_that("browser-folder" in body, "the docs do not use a folder that exists here")
    assert_that("circe mcp browser-folder" in body, "the docs are missing the MCP install command")
    assert_that("/plugin marketplace add " + base in body, "the docs are missing the marketplace command")

    # Every section the contents row points at has to exist.
    for anchor in ("start", "install", "commands", "mcp", "rules", "skills", "backup", "api"):
        assert_that(
            page.locator(f"#{anchor}").count() == 1,
            f"the docs link to #{anchor} but there is no such section",
        )

    # And the copy buttons are wired to the command beside them.
    copyable = page.locator("[data-copy]")
    assert_that(copyable.count() >= 10, f"only {copyable.count()} copyable commands on the docs page")


def docs_is_in_the_nav(page, base):
    page.goto(base + "/skills")
    link = page.locator('nav[aria-label="Main"] a[href="/docs"]')
    assert_that(link.count() >= 1, "Docs is not in the main nav")


def sync_connect_and_disconnect(page, base):
    page.goto(base + "/settings")
    assert_that("Keep it all in a repository" in page.content(), "the sync section is missing from settings")

    fill_form(page, "/sync/connect", {
        "repo": "https://github.com/octocat/hello-world",
        "branch": "main",
    })
    submit(page, "/sync/connect")

    body = page.content()
    assert_that("octocat/hello-world" in body, "the connected repository is not shown")
    assert_that("Push" in body, "there is no push button once connected")

    # Pushing with no token must fail in words a person can act on, and must
    # not lose the connection.
    submit(page, "/sync/push")
    body = page.content()
    assert_that("token" in body.lower(), f"pushing without a token should explain why, got: {flash_of(body)}")
    assert_that("octocat/hello-world" in body, "a failed push must not disconnect the repository")

    page.once("dialog", lambda d: d.accept())
    submit(page, "/sync/connect")
    assert_that(
        'name="repo"' in page.content(),
        "after disconnecting, the connect form should be back",
    )


def flash_of(body):
    match = re.search(r'class="flash[^"]*"[^>]*>(.*?)<', body, re.S)
    return match.group(1).strip() if match else "(no flash found)"


def install_command_downloads(page, base):
    """The CLI's own endpoint, which is what every install actually uses."""
    url = base + "/api/v1/download?skill=browser-made"
    req = urllib.request.Request(url)
    req.add_header("Authorization", "Bearer " + PASSCODE)
    with urllib.request.urlopen(req, timeout=20) as r:
        assert_that(r.status == 200, f"download returned {r.status}")
        payload = r.read()
    assert_that(len(payload) > 0, "the download was empty")
    assert_that(payload[:2] == b"\x1f\x8b", "the download is not gzip")


def pocketbase_dashboard_needs_the_registry_password(page, base):
    """The dashboard is a second way into the same data, so it is behind the
    same door. A stranger should not even reach its sign-in page."""
    ctx = page.context.browser.new_context()
    other = ctx.new_page()
    try:
        other.goto(base + "/_/")
        other.wait_for_load_state("networkidle")
        assert_that(
            path_of(other.url) == "/login",
            f"an unauthenticated browser reached the dashboard at {other.url}",
        )
        assert_that(
            "superuser" not in other.content().lower(),
            "the dashboard's own sign-in page was rendered to a stranger",
        )
    finally:
        ctx.close()


def pocketbase_dashboard_loads_once_signed_in(page, base):
    page.goto(base + "/_/")
    page.wait_for_load_state("networkidle")
    assert_that(
        path_of(page.url).startswith("/_/"),
        f"a signed-in browser was refused the dashboard, landed on {page.url}",
    )
    assert_that(
        "pocketbase" in page.content().lower() or "superuser" in page.content().lower(),
        "the PocketBase dashboard did not load",
    )


def pocketbase_api_is_not_public(page, base):
    """The registry's own data must not be readable through PocketBase's API,
    and the refusal should come from the registry rather than from PocketBase."""
    for path in ("/api/collections/skills/records", "/api/health"):
        req = urllib.request.Request(base + path)
        try:
            with urllib.request.urlopen(req, timeout=10) as r:
                raise AssertionError(f"{path} answered {r.status} without auth")
        except urllib.error.HTTPError as err:
            assert_that(
                err.code in (401, 403),
                f"{path} should refuse anonymous requests, got {err.code}",
            )

    # Circe's own health check has to stay public, or platform probes break.
    with urllib.request.urlopen(base + "/api/v1/health", timeout=10) as r:
        assert_that(r.status == 200, f"/api/v1/health returned {r.status}")


def delete_skill(page, base):
    page.goto(base + "/skills/browser-made")
    page.once("dialog", lambda d: d.accept())
    submit(page, "/skills/browser-made/delete")

    page.goto(base + "/skills")
    assert_that("browser-made" not in page.content(), "the deleted skill is still listed")

    # And it is gone from the folder that held it, rather than leaving the
    # folder promising something that no longer exists.
    page.goto(base + "/bundles/browser-folder")
    assert_that("browser-made" not in page.content(), "the folder still references the deleted skill")


def every_page_renders(page, base):
    for path in ("/", "/skills", "/rules", "/mcp", "/bundles", "/docs", "/settings", "/import/repo", "/import/file"):
        response = page.goto(base + path)
        assert_that(response is not None, f"no response for {path}")
        assert_that(response.status == 200, f"{path} returned {response.status}")


# ---------- runner ----------


def main():
    tmp = tempfile.mkdtemp(prefix="circe-e2e-")
    binary = os.path.join(tmp, "circe-server")
    build_server(binary)

    port = free_port()
    base = f"http://127.0.0.1:{port}"
    env = dict(os.environ, CIRCE_PASSWORD=PASSCODE)
    # A token from the developer's own shell would turn the "pushing without a
    # token" check into a real API call against someone else's repository.
    env.pop("GITHUB_TOKEN", None)
    env.pop("GH_TOKEN", None)

    log = open(os.path.join(tmp, "server.log"), "w+")
    proc = subprocess.Popen(
        [binary, "-addr", f"127.0.0.1:{port}", "-data", os.path.join(tmp, "data")],
        env=env,
        stdout=log,
        stderr=subprocess.STDOUT,
    )

    results = Results()
    try:
        wait_until_up(base)
        print(f"server up at {base}\n", flush=True)

        with sync_playwright() as pw:
            browser = pw.chromium.launch()
            context = browser.new_context()
            page = context.new_page()

            console_errors = []
            page.on("console", lambda m: console_errors.append(m.text) if m.type == "error" else None)
            page.on("pageerror", lambda e: console_errors.append(str(e)))

            checks = [
                ("a wrong passcode is refused", wrong_passcode_is_refused),
                ("signing in", sign_in),
                ("creating a skill", create_skill),
                ("the skill is listed and searchable", skill_is_listed_and_searchable),
                ("editing a skill", edit_skill),
                ("adding a companion file", add_companion_file),
                ("creating a rule", create_rule),
                ("creating an MCP server", create_server),
                ("a folder holding all three", create_folder_with_all_three),
                ("the marketplace manifest", folder_serves_a_marketplace_manifest),
                ("Docs is in the nav", docs_is_in_the_nav),
                ("the docs point at this registry", docs_page_is_pointed_at_this_registry),
                ("connecting and disconnecting a sync repository", sync_connect_and_disconnect),
                ("the CLI download endpoint", install_command_downloads),
                ("the dashboard needs the registry password", pocketbase_dashboard_needs_the_registry_password),
                ("the dashboard loads once signed in", pocketbase_dashboard_loads_once_signed_in),
                ("PocketBase's API is not public", pocketbase_api_is_not_public),
                ("deleting a skill", delete_skill),
                ("every page renders", every_page_renders),
            ]
            for name, fn in checks:
                results.check(name, lambda fn=fn: fn(page, base))

            results.check(
                "no console errors",
                lambda: assert_that(not console_errors, f"console errors: {console_errors[:5]}"),
            )

            browser.close()
    except PlaywrightError as err:
        print(f"playwright failed: {err}")
        results.failed.append(("playwright", err))
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
        log.seek(0)
        server_log = log.read()
        log.close()

    code = results.report()
    if code:
        print("\n--- server log ---")
        print(server_log)
    return code


if __name__ == "__main__":
    sys.exit(main())

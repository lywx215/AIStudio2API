# Business login collection

`Collect(ctx, Options, Launcher) (vault.Credential, error)` keeps the existing
credential shape. A nil launcher uses `Launch`; offline callers can inject a
fake driver. A successful return means the disposable browser has closed and
its own profile has been removed. `ErrCleanup` discards the entire snapshot.

`Manager` has a usable zero value and exports the same `Collect` method,
nonblocking `Cancel()`, and bounded `Close(ctx) error`. It permits one collection
at a time and returns `ErrBusy` while collection or cleanup is active. Cleanup
failure blocks subsequent collections in that manager. Closing prevents new
collections with `ErrClosed` and still reports any cleanup failure.

The host must supply its authenticated console-session context, its shutdown
context, a deadline appropriate for human login, and an absolute Business-only
profile root in `Options.Directory`. It must apply vault revision/account epoch
checks before saving. This package does not persist accounts or enable them.

Collection opens a local visible or headless browser with a fresh `collect-*` profile. It
does not install a browser, inspect old AI Studio profiles, load old Camoufox
fingerprint environment settings, or call the old Login/Worker. Private browser
temporary files live under the new profile. The Windows process starts suspended
and is assigned to its own non-breakaway job before it can spawn children.
Shutdown first closes the BiDi socket, then terminates and confirms that job
(or the private POSIX process group), waits for the launcher process, and only
then removes the exact profile created by that call. Uncertain termination
leaves the profile in place and reports `ErrCleanup`.

Supported collection is deliberately narrow:

- WebDriver BiDi must identify the explicitly tested `camoufox 152.0.4-beta.29`
  release. Unknown names/versions return `ErrVersion` before Business navigation.
  `Open` also probes viewport, script/preload, cookies, screenshot and input.
- The only navigation is `https://business.gemini.google/`; the user performs
  login. Explicit current-URL `/cid/<config>` and `csesidx` fields provide
  metadata. Unknown layouts remain pending and eventually return `ErrMissing`
  with the caller's deadline error.
- `storage.getCookies` reads HttpOnly cookies from that browsing context. The
  collector checks origin, cookie domain, secure flag, path, expiration and
  conflicting duplicates. URL and cookies are read again to reject changes
  during the snapshot. `Session` also exposes bounded screenshots and manual
  click/key/text commands; no arbitrary script/URL API is exposed to callers.
- `observer.js` observes the page's existing fetch/XHR calls and returns only
  recognized model selectors. Observations are pending evidence, never automatic
  configuration or verified generation. No model IDs are guessed and no model
  probes are sent. Browser generation transport (C06) remains absent.
- Headless capture is available; Linux without a display selects headless.
  The Host owns authenticated task binding, expiry, idle cancellation and CAS.

Offline validation uses synthetic drivers/connections and temporary profiles:

```text
go test -mod=readonly ./internal/addons/business/browser
go vet ./internal/addons/business/browser
```

These checks do not launch Firefox/Camoufox, use Google accounts, prove a live
login layout, or exercise the real operating system process job. Race testing
also requires a working CGO compiler in the execution environment.

`BUSINESS_BROWSER_TEST_EXECUTABLE` explicitly opts into a local headless test
using an inline HTML fixture only. The 152.0.4-beta.29 Windows release passed
screenshot, keyboard input and profile cleanup in this test. This does not
prove manual Google login or a no-desktop Linux VPS deployment.

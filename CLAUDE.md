# Local media server

The project plan is in `PLAN.md`. Read it before doing any work. It defines the milestones (M0 to M9), the technical design, the acceptance tests for each milestone, and the rules that hold throughout.

Status: M0 passed the owner's check on a Windows PC (2026-11-11). M1 (the library) is finished apart from two items recorded as not done in `docs/decisions.md` (the Chromium 87 check, and Huma/sqlc) and is waiting for the owner's checklist (`docs/checklists/M1.md`). M2 (installer, tray icon, automatic updates with backup and undo) is built and tested; no release has been published yet, so automatic updates have not yet run for real. M3 (plays most things: repackaging and sound conversion while watching, sound and subtitle choice, the "why is this converting?" panel) is built and tested and is waiting for the owner's checklist (`docs/checklists/M3.md`); what was not done is at the end of the M3 entry in `docs/decisions.md`. Next: M4 (converting the picture), which the owner was advised to run at a higher effort level.

Build environment: this project has its own cloud environment ("Local-media-project") that may reach `api.themoviedb.org` and `image.tmdb.org`; the owner's TMDB token is saved on it and added to requests on the way out. `LM_LIVE_TMDB=1 go test ./internal/metadata -run TestLive` talks to the real TMDB.

Owner's answers so far (2026-11-11):
- The phone is an iPhone. Safari's engine is checked on a macOS runner from M0, Safari on iOS is a first-class target from M3, and Streamyfin and Infuse are the first store apps in M5.
- The repository is public under GPL-3.0-or-later (the owner made it so on 2026-11-12; checked without a login). Windows, macOS and ARM Linux checks therefore run on every push.
- Releases: the owner allows version tags (`vX.Y.Z`) to be pushed (2026-11-12). Pushing one publishes a release, which installed copies then fetch by themselves, so tag only a commit whose checks are green on all systems, and write `docs/release-notes/<tag>.md` first. Everything else still goes only to the working branch.
- No Jellyfin on the owner's PC. Do not plan around recordings from, or comparisons with, a Jellyfin of theirs.
- Not yet answered: everything else under "Questions about your setup" in PLAN.md (defaults apply), plus the TMDB key (item 9) and the app's name (item 10). The one that changes the order of the work is question 1: the server PC's operating system (Windows 11 assumed) and whether there is a TV to support (none assumed).

Working rules:
- The owner is not a developer. Anything written for them (checklists, release notes, the top half of the plan) uses plain words and explains any term it needs.
- Record decisions made along the way in `docs/decisions.md`, and update the status line above when a milestone ships.
- Before anything lands: `make verify` (Go tests, then the keyboard-only browser test). Put `[all-os]` in a commit message to also run the Windows and macOS checks at GitHub while the repository is private.
- The converter tests and the browser test need ffmpeg and ffprobe (on the PATH here; at GitHub the pinned build in `packaging/ffmpeg.env`). `internal/hls` is where files are repackaged; read its package comment before changing how chunks are cut.
- Do not install Playwright in this environment; `e2e/run.mjs` uses the preinstalled one. Do not put cleanup commands that delete folders into scripts or the Makefile.
- Never commit anything from the owner's home: file lists, support bundles, recorded app requests, device names, addresses. The repository is public or about to be, so this covers PLAN.md and this file too: record the owner's answers only as kinds of device and choices, never names or details of the household.

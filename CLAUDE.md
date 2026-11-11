# Local media server

The project plan is in `PLAN.md`. Read it before doing any work. It defines the milestones (M0 to M9), the technical design, the acceptance tests for each milestone, and the rules that hold throughout.

Status: M0 passed the owner's check on a Windows PC (2026-11-11). M1 (the library) is in progress: name recognition, the library itself and online posters and descriptions are done (slices 1 to 3 in `docs/decisions.md`); what remains is listed at the end of slice 3 there.

Build environment: this project has its own cloud environment ("Local-media-project") that may reach `api.themoviedb.org` and `image.tmdb.org`; the owner's TMDB token is saved on it and added to requests on the way out. `LM_LIVE_TMDB=1 go test ./internal/metadata -run TestLive` talks to the real TMDB.

Owner's answers so far (2026-11-11):
- The phone is an iPhone. Safari's engine is checked on a macOS runner from M0, Safari on iOS is a first-class target from M3, and Streamyfin and Infuse are the first store apps in M5.
- The repository is to be public under GPL-3.0-or-later. Only the owner can change its visibility; check whether they have done so before relying on free Windows and macOS runners.
- No Jellyfin on the owner's PC. Do not plan around recordings from, or comparisons with, a Jellyfin of theirs.
- Not yet answered: everything else under "Questions about your setup" in PLAN.md (defaults apply), plus the TMDB key (item 9) and the app's name (item 10). The one that changes the order of the work is question 1: the server PC's operating system (Windows 11 assumed) and whether there is a TV to support (none assumed).

Working rules:
- The owner is not a developer. Anything written for them (checklists, release notes, the top half of the plan) uses plain words and explains any term it needs.
- Record decisions made along the way in `docs/decisions.md`, and update the status line above when a milestone ships.
- Before anything lands: `make verify` (Go tests, then the keyboard-only browser test). Put `[all-os]` in a commit message to also run the Windows and macOS checks at GitHub while the repository is private.
- Do not install Playwright in this environment; `e2e/run.mjs` uses the preinstalled one. Do not put cleanup commands that delete folders into scripts or the Makefile.
- Never commit anything from the owner's home: file lists, support bundles, recorded app requests, device names, addresses. The repository is public or about to be, so this covers PLAN.md and this file too: record the owner's answers only as kinds of device and choices, never names or details of the household.

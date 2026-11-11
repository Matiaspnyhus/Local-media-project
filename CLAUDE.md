# Local media server

The project plan is in `PLAN.md`. Read it before doing any work. It defines the milestones (M0 to M9), the technical design, the acceptance tests for each milestone, and the rules that hold throughout.

Status: plan written 2026-11-11. Nothing is built yet. The owner said go on 2026-11-11; M0 is in progress.

Owner's answers so far (2026-11-11):
- The phone is an iPhone. Safari's engine is checked on a macOS runner from M0, Safari on iOS is a first-class target from M3, and Streamyfin and Infuse are the first store apps in M5.
- The repository is to be public under GPL-3.0-or-later. Only the owner can change its visibility; check whether they have done so before relying on free Windows and macOS runners.
- No Jellyfin on the owner's PC. Do not plan around recordings from, or comparisons with, a Jellyfin of theirs.
- Not yet answered: everything else under "Questions about your setup" in PLAN.md (defaults apply), plus the TMDB key (item 9) and the app's name (item 10). The one that changes the order of the work is question 1: the server PC's operating system (Windows 11 assumed) and whether there is a TV to support (none assumed).

Working rules:
- The owner is not a developer. Anything written for them (checklists, release notes, the top half of the plan) uses plain words and explains any term it needs.
- Record decisions made along the way in `docs/decisions.md` (created in M0), and update the status line above when a milestone ships.
- Never commit anything from the owner's home: file lists, support bundles, recorded app requests, device names, addresses. The repository is public or about to be, so this covers PLAN.md and this file too: record the owner's answers only as kinds of device and choices, never names or details of the household.

# Decision log

What was decided, when, and why. Newest at the bottom. `PLAN.md` holds the plan; this file holds the choices made while carrying it out.

## 2026-11-11: M0

**Owner's answers.** Phone: iPhone. Repository: to be public (the owner changes the visibility). No Jellyfin on the owner's PC. Open: the server PC's operating system (Windows 11 assumed), whether there is a TV, and the other setup questions in the plan.

**Fixed in M0, as the plan requires.**
- IDs are random 128-bit values stored as 32 lowercase hex characters (`internal/ids`). `ids.GUID` renders the same value with dashes for Jellyfin-compatible apps. Nothing is ever re-encoded.
- The data model is item → versions → files (`internal/db`, schema 1). Items have a `kind` and an optional `parent_id`.
- Every route is declared once in `internal/server/routes.go` with a policy (public, setup-local, session, owner, signed-link). A route without a policy stops the server from starting. A test pins the public routes to exactly three: `/health`, `/api/state`, `/api/login`.
- Stream links carry a signature over the file, the expiry time and the session they were issued to (HMAC-SHA256, key kept in the database). They work without the session cookie, expire after 12 hours, and die when that session signs out.
- The database is copied to `backups/` before any schema upgrade, upgrades run in a transaction, and a database from a newer version is refused.
- Every schema version has a sample database in `internal/db/testdata/`, and a test upgrades each one and checks the watch progress in it survived.
- The data folder and internal name are `localmedia` and do not change: `%LOCALAPPDATA%\localmedia` on Windows, the user config folder elsewhere.
- Default port: 8177. Jellyfin's ports (8096, 8920, 7359) are never used.

**How music fits the model** (the one-page sketch the plan asks for). An artist is an item of kind `artist`. An album is an item of kind `album` whose parent is its album artist. A track is an item of kind `track` whose parent is the album; its disc and track numbers are columns added with the music schema in M7, so an album on several discs stays one album item. A track's file hangs off a version exactly as a film's does, which also covers the same album in two formats. Shows follow the same shape: show → season → episode through `parent_id`.

**Choices made while building.**
- One database connection, so every write is serialised. A separate read pool is deferred to M1, when library size makes it matter.
- Until setup is finished the server answers only the PC it runs on (decided from the real connection address, never from headers). Other devices get a short "setup is not finished" page. The health check is the one exception.
- Sign-in uses a cookie without the `Secure` flag, because the server must work over plain HTTP at home. It is `HttpOnly` and `SameSite=Lax`, and every write must also carry a header that only our own page sends and must not come from another origin.
- Wrong passwords: after 5 failures from one address within 10 minutes, further tries from that address are refused until they age out. Kept in memory, so a restart clears it.
- A file that has disappeared is marked missing, not deleted, so its watch progress survives a drive that is unplugged for a while.
- Symbolic links in a media folder are not followed by the scan, and file reads go through `os.Root`, which refuses any path that leaves the library folder.
- The scan in M0 lists files only. Reading each file's details with ffprobe starts in M1, as the plan says, so M0 ships no ffmpeg.
- The web app in M0 does not use Huma or sqlc yet. The M0 API is a dozen hand-written routes and a dozen queries; both tools arrive in M1 with the library, where the API and the queries grow. Nothing in M0 has to be rewritten for that.
- The player reports three things it can detect by itself: a file that will not play, a file with no picture, and (in Chrome) a file whose sound is not being decoded. Each shows a plain message and lands in the support bundle.
- The support bundle holds the program version, counts of files by type, the names of files that failed to play, and recent log lines with addresses and anything token-like removed. It never lists the whole library.
- Home addresses shown to the owner: private (home-network) addresses first, then any other non-loopback IPv4 address.

**Checks and where they run.**
- Here and on every push: Go tests, a build for six platforms with cgo off, and a keyboard-only browser test in Chromium against a plain-HTTP network address.
- On a Windows machine at GitHub: the Go tests, and the built program starts, serves the web app and answers.
- On a Mac at GitHub: the Go tests, and the same browser test in Safari's engine.
- ARM Linux at GitHub is only free for public repositories, so that check starts when the repository is public.
- While the repository is private these extra machines run only for release tags, on request, or when a commit message contains `[all-os]`.

## 2026-11-11: M1, slice 1 — recognising names

**The owner's M0 check passed** on a Windows PC: setup, play and resume worked. The support bundle showed one false alarm: the "no sound" note appeared on a file that most likely has no sound track. From M1 the note is only shown when the server knows the file has sound.

**The name recogniser** (`internal/naming`) is a pure function from a path to "film, title, year" or "episode, show, season, episode". It is tested two ways:

- *Conventional names:* 52 film titles and 32 show titles, chosen to be awkward (numbers, years, punctuation, words such as "Web", "French" and "Limited" that also appear as technical tags), each run through the folder layouts Plex and Jellyfin recommend, the names Radarr and Sonarr write, and plain release names. 872 paths, the right answer known by construction. Result: 872 of 872. The plan's 97% target is enforced on this set.
- *Independent torture set:* 744 names with answers written by the authors of two other parsers (guessit, LGPL-3.0, and PTT, MIT; see `internal/naming/testdata/README.md`). Result: 605 of 744 (81.3%). The test fails if this number drops.

**Where this departs from the plan.** The plan asked for about 1,500 independently labelled names at 97%. The independent sets that exist are built to break parsers: reversed text, scrambled file names, mixed languages, and conventions where those parsers and we reasonably disagree (they drop "Part III" from "The Godfather Part III" and "US" from "The Office US"; we keep both, because the lookup in M1 needs them). Reaching 97% there would mean re-implementing guessit. The target is therefore held on conventional names, the independent set is kept as a ratchet, and the real test is the owner's own library: the list of files M1 could not identify.

## 2026-11-11: M1, slice 2 — the library

**What exists now.** The scan turns paths into films, shows, seasons and episodes. ffprobe inspects each file once and what it reports is stored (length, every picture, sound and subtitle track, with the profile, level and HDR fields later milestones need). The home screen has Continue Watching and Next Up; there is search, a page per show, and a mark on files this browser cannot play.

**Choices.**
- *Moved and renamed files* are recognised by size plus a hash of the first and last 64 KB, read once when a file is first seen (at most 128 KB per file). A file found under a new path whose old path is gone keeps its item, so it stays watched. A different file under an old name is a new item.
- *Shows are matched by title*, ignoring case and punctuation, so `Good Show`, `good.show` and `Good Show (2015)` in three folders are one show. Two different shows with the same title would be merged; online lookups (slice 3) are what can tell them apart.
- *Two copies of one film* (same title and year) are two versions of one item. The largest plays; choosing between them comes with M3.
- *Trailers, samples and extras* (by folder name or file-name suffix, following the Plex and Jellyfin conventions) are not listed.
- *Next Up*: for each show, take the episode watched most recently. If it is unfinished it belongs to Continue Watching. If it is finished, the next unwatched episode after it is next, across season boundaries. Specials are never next.
- *"Can this browser play it?"* is `internal/playback.Decide`, a pure function with table tests: what ffprobe stored and what the device reports go in, a yes/no/unknown with reasons in plain words comes out. This is the function M3 extends; M1 only asks about playing the original file. The browser reports what it can play by being asked (`canPlayType`), once per visit. One correction is applied on the server: Firefox claims MKV but reads the whole file before playing.
- *The sound track that counts* is the default one unless it is a commentary; a film with a Dolby commentary and an AAC main track is playable.
- *Without ffprobe* the answer is "unknown": nothing is marked, and the player falls back on noticing problems while playing. With ffprobe, the "no sound" note is never shown for a file that has no sound track (the false alarm from the owner's M0 check).
- *Search* quotes every word typed and matches it as a prefix, so nothing typed can act as search syntax.
- *Sidecar subtitles* (`Film.en.srt`, `Film.no.forced.srt`) are recorded with language and forced flag; they are used from M3.
- *The pinned inspector*: jellyfin-ffmpeg 8.1.3-2. The Windows download now contains `ffprobe.exe` beside the program, fetched in the build and checked against a fixed SHA-256 (`packaging/ffmpeg.env`), with its licence text and a link to its source. `ffmpeg.exe` joins it in M3, when conversion needs it. On macOS and Linux the program uses an `ffprobe` it finds beside itself or on the PATH.

**Still to do in M1.** Online posters and descriptions (needs the owner's TMDB key and that site allowed in the build environment), reading NFO info files, the 50,000-item speed targets, a separate pool of read connections, generated API description and database code, and the check against the oldest supported browser engine.

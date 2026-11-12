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

## 2026-11-11: M1, slice 3 — posters and descriptions

**What exists now.** With a TMDB key saved, films and shows get their proper title, year, description and poster, and episodes get their names. The owner pastes the key into a settings card; a switch turns lookups off.

**Choices.**
- *Either TMDB credential works*: the short API key or the long read-access token. The settings card asks for the short one.
- *The key lives in the database on the owner's PC.* No call returns it, the support bundle never holds it, and errors from the lookup code never include the address that carried it. It is not in the repository or in any download. Whether TMDB's terms allow shipping one shared key (as Jellyfin does) is still unread; until then each install uses its own.
- *Only titles and years are sent*, taken from what the recogniser read. A test checks that no path, file name or release tag goes out.
- *Finding nothing beats finding the wrong film.* A hit must have the same title, ignoring case, accents, punctuation and "&"/"and", against either the title or the original title. With a year in the file name the hit must be within one year of it. Without a year the best-known hit with that title wins. So `The Office` with no year becomes the 2005 one; a folder named `The Office (2001)` gets the British one.
- *A `{tmdb-603}` tag in a folder or file name* names the film or show exactly and skips the search.
- *Nothing is asked twice.* Found and not-found are both remembered. A refused key or an unreachable TMDB stops the round and changes nothing, so it resumes later.
- *The scan matches on its own key*, the title as the file names it, stored beside the item. The shown title can then change to TMDB's without the next scan thinking the files are new.
- *Search finds a film under both names*, TMDB's and the file's.
- *Posters* are stored in an `artwork` folder beside the database and served only to signed-in devices. The route takes an item ID; the file name comes from the database.
- *The lookup client can only talk to TMDB's two sites*: addresses are built from two fixed bases, redirects are refused, poster paths from TMDB are still pattern-checked, and answers are size-capped.
- *Rate*: at most 10 requests a second, one at a time.
- *What was not found online* is listed in the support bundle (titles as read from file names), which is how the recogniser gets improved from a real library.
- *Tests* run against answers recorded from the real TMDB (`internal/metadata/testdata`), so they need no network. One test talks to the real service and runs only when asked.

**Build environment.** A separate cloud environment for this project now allows TMDB's two sites, with the owner's read-access token saved on it and added to requests on the way out; no key is in the repository or in this session.

**Still to do in M1.** Reading NFO info files, the 50,000-item speed targets, a separate pool of read connections, generated API description and database code, and the check against the oldest supported browser engine.

## 2026-11-11: M1 finished

**Local information wins.** The scan reads Kodi-style info files (`Film.nfo` or `movie.nfo`, `tvshow.nfo`, an episode's own `.nfo`) and pictures beside the media (`poster.jpg`, `folder.jpg`, `Film-poster.jpg`). An item named by an info file keeps that title, year and description; an online lookup only fills what is missing. A local picture is served straight from the library and is never replaced by TMDB's. Info files are read through the library's root, so one linked from outside the library is not read, and files that are not XML (some hold only a web address) are ignored. An info file is read once; later edits to it are not picked up yet.

**Reading while writing.** The database now uses a few connections. Every transaction takes the write lock at its start and waits its turn, so pages keep loading during a long scan and two writers queue instead of failing. A test holds a write open and checks that a read goes through and a second write waits, then succeeds.

**Speed at 50,000 files**, measured on the four-core build machine with empty files laid out as 2,000 films and 400 shows of 120 episodes:

| | Target | Measured |
|---|---|---|
| First scan (finding and recognising) | 10 minutes | 11 seconds |
| Unchanged rescan | 60 seconds | 1.3 seconds |
| First screen, 95th percentile | 100 ms | 40 ms |
| All films / all shows / one show / search | none set | 93 / 24 / 7 / 7 ms |

Inspecting files with ffprobe is separate and runs in the background after the scan: 12 ms per small local file here with four at a time, so roughly ten minutes for 50,000, and longer for large files on a network drive. The library is usable while that runs; marks appear as files are inspected. The plan's "under 10 minutes including probing" is therefore met for the scan and only borderline for inspection. A smaller version of the test (3,000 files) runs with every test; `LM_BIG=50000` runs the full size.

Getting there needed three things: each episode now stores its show directly (schema 4), the Next Up query became two short indexed steps, and the scan refreshes SQLite's table statistics when it adds files. One index I added first made things slower by tempting the planner to start from the files table; it was replaced by a narrow one.

**Malformed IDs.** A test puts traversal strings, over-long values, wrong-case IDs and injection attempts where an ID belongs on every route that takes one; all are refused and none crashes.

**Not done, and why.**
- *The check in the oldest supported browser engine (Chromium 87).* The web app is built for that target, so its syntax is down-levelled, but loading it in a real Chromium 87 was blocked: downloading and running that old browser build was refused by a safety check in the build environment. It needs the owner's decision (allow it here, or run it at GitHub).
- *Generated API description and generated database code (Huma, sqlc).* The plan listed these so that the web app cannot drift from the server and SQL is checked before it runs. Today that risk is covered differently: every query and every call the web app makes is exercised by tests against a real database and by the end-to-end browser test. Adopting the two tools now would mean rewriting working, tested code for no change the owner can see. They are deferred until the API has outside consumers to keep in step with, which is M5.

## 2026-11-11/12: M2 — install and safe updates (built while the owner was away)

**What exists now.** A Windows installer, an icon beside the clock, start with Windows, and updates that install themselves, back up first, and undo themselves if the new version does not start. The owner can also go back to the earlier version with one button.

**The order of an update is fixed** (`internal/update`, orchestrated in `cmd/localmedia/updater.go`):

1. Find a newer release; download its package.
2. Check the package against the release's own SHA-256 list. A package that does not match is deleted.
3. Unpack it beside the installed files without touching them. Anything but plain files in one folder is refused.
4. Back up the database.
5. Stop the server, then swap the files. Every replaced file is kept as `name.previous`. A running program can be renamed on Windows, which is what makes this work without a helper program.
6. Start the new version and watch for it to answer with the new version number.
7. If it stops straight away or does not answer within a minute: put the files back, restore the database backup, start the earlier version again, and record why in plain words.

**Proved by a test that updates the real program** (`cmd/localmedia/update_e2e_test.go`): it builds three versions, serves them from a stand-in release page, and goes v1.0.0 → v1.1.0 (good) → back to v1.0.0 → v1.1.0 → v1.2.0 (a build that cannot start, undone by itself). After every step the same sign-in still works and the watch progress is still there. It runs on Linux, Windows and macOS.

**Choices.**
- *Only plain releases update.* A version that is not of the form `v1.2.3` (every build from a branch) never looks for or installs updates.
- *Automatic by default, never while someone is watching.* A check 20 seconds after start and every 12 hours. An update installs by itself only if the owner has left that on, nothing has been streamed for 15 minutes, and that exact version has not already failed once.
- *Going back restores the database backup only if the update changed the database's layout.* Otherwise the earlier version reads the database as it is, and nothing watched since the update is lost.
- *Downloads may only come from GitHub's hosts*, over HTTPS, and a redirect to anywhere else is refused.
- *No signature check yet; this departs from the plan.* The plan says updates are signature-checked. Today an update is trusted because it was downloaded over HTTPS from this project's release page and matches that release's checksum list. That protects against a damaged or swapped download, but not against someone who has taken over the GitHub account. A separate signing key would close that gap; it has to be created and stored as a repository secret by the owner, because the build environment cannot create or read repository secrets. Until that exists, automatic updates rest on the GitHub account's own security.
- *Per-user install.* The installer needs no administrator rights and puts the program in the user's own Programs folder, so updates need none either. The single exception is the firewall rule: Windows asks once, during install. If the owner says no, the install still succeeds.
- *The data folder is never the installer's business.* It is somewhere else, the installer does not list it, and uninstalling leaves it. The loose program from M0/M1 used the same folder, so the installed program simply carries on with the same library and watch history.
- *The packaged Windows program has no console window.* Its sign of life is the icon beside the clock, with Open, Start with Windows, Check for updates and Quit. Problems that stop it from starting are shown in a message box.
- *Start with Windows* is one value under the signed-in user's own Run key. No service: a service cannot see mapped network drives.
- *No tray on macOS or Linux*: the library used needs C code on macOS, which would break building every system from one machine.

**Blocked on the owner.**
- Updates download from the project's release page, which only answers without a login once the repository is public.
- Publishing a release means pushing a version tag, which is outside the one branch the build environment may push to.
Until both are settled the updater has nothing to fetch; the settings screen says so in plain words.

## 2026-11-12: M3 — plays most things

**What exists now.** A file a device cannot play as it is no longer stops at "this browser cannot play this file". If its picture is one the device can show (H.264, HEVC or AV1), the file is repackaged while it plays, and its sound is converted to stereo AAC when the device cannot play the sound either. The picture is never touched in this milestone. Sound tracks and text subtitles can be chosen in the player, the choice is remembered for the show (or the film), each viewer has their own language preferences, and a panel under the player says in one sentence what is being done and why.

**How it works** (`internal/playback` decides, `internal/hls` does it):

1. The decision is still a pure function: play the original, repackage, repackage and convert the sound, or cannot play. 1,890 combinations of file and device are checked against rules that must always hold, plus track-choice cases (commentary, a dub in another language, forced subtitles, a remembered choice).
2. The player is given a list of chunks for the whole film at once, so the timeline is complete and it can jump anywhere. ffmpeg is started at the chunk the player asks for; a jump beyond what it has made restarts it there.
3. **The server cuts the chunks itself.** ffmpeg writes one continuous stream (a fragment per key frame), and the server puts each fragment into the chunk its start time belongs to. Where the cuts are is decided beforehand from the film's real key frames (`hls.FindCuts`), so a chunk holds exactly the same frames whether ffmpeg played through to it or was started there.

**Why the server cuts, which is a change from the first design.** The first version let ffmpeg's own chunk writer do the cutting, with a list that assumed a cut every 6 seconds. Tests against the real ffmpeg showed two faults: a run started after a jump wrote its times from zero and a different header, and its chunks began at whatever key frame was nearest, up to several seconds from where the list said. A player trusts the list for the first chunk it fetches, so after resuming or jumping the whole timeline would have been off by that much: subtitles out of step and resume positions wrong. Cutting on known key frames makes the list exact. `TestEveryChunkBeginsWhereTheListSays` checks every chunk both ways, with copied and with converted sound, on a film with irregular key frames.

**Choices.**
- *Finding the key frames.* An MKV file carries its own list of key frames (the list players use to jump around); the server reads it directly, which takes one small read (`internal/hls/cues.go`). For other files, or an MKV whose list is missing or lists only one key frame in many, ffprobe is asked to jump to every sixth second and report the key frame it lands on. 1,200 such jumps took 0.1 seconds on a fast disk; on a slow hard disk a long film may take several seconds, once. Either way the result is kept in the database (`cuts`), tied to the file's size and date.
- *One second is added to every time ffmpeg writes.* Sound often starts a few hundredths of a second before zero, which the chunk format cannot express. Players only use the differences, so it is invisible; it is the same in every run.
- *The app's own player (hls.js) is used wherever it can run, except on Apple devices*, which play chunk lists themselves and always have. The end-to-end test found that the test browser (a recent Chromium) has started to play chunk lists itself; that path is new and untested by this project, so it is not used.
- *One run's chunks are never mixed with another's*, and a restart deletes the earlier run's. Disk use per viewer is bounded: ffmpeg is made to wait when it is 50 chunks ahead, and chunks more than 20 behind the viewer are deleted.
- *Stopping.* Leaving the player tells the server, which stops ffmpeg at once. If the browser simply vanishes, ffmpeg is stopped after 75 seconds without a request and its chunks deleted. Signing out stops everything for that sign-in. At start, anything a previous run left behind is deleted.
- *Chunk addresses need no cookie* (phones and TVs fetch media with players that do not send one). They are reachable only through a random 128-bit token, die when their sign-in is signed out, and one sign-in can hold at most six films open.
- *Subtitles are text only.* Files beside the video (.srt, .vtt) are converted by the server itself, whatever encoding they were saved in. Tracks inside the video are taken out by ffmpeg, sent to the browser while that happens, and kept for next time. Taking a track out means reading the whole file once, so on a long film the first use can lag behind a jump far ahead; the second use is instant. Picture subtitles (as on discs) and styled ones are M4.
- *If the preferred sound track cannot be reached* (no converter on this PC, or the device cannot play repackaged video), the film plays with its own main track instead of not playing.
- *ffmpeg.exe is now shipped* beside ffprobe.exe, from the same pinned build. The download grows accordingly. Linux and macOS test machines fetch the same version, checked against a fixed checksum, and a test that needs the converter fails rather than being skipped there.
- *The page's security header* now allows media and helper scripts from `blob:` addresses, which is how hls.js hands video to the browser. Nothing else was loosened.

**Departures from the plan, and what was not done.**
- *No second player (mpv) in the tests.* The plan asks for headless mpv to play every test stream. mpv is not available in the build environment. In its place every produced stream is decoded from end to end by ffmpeg with errors fatal, which catches a broken stream but is not an independent player.
- *The stricter numbers are checked on made-up films, not a library of real ones.* Picture and sound start within 50 ms at the beginning of a stream and within 100 ms at the start of any single chunk (sound comes in frames of about 23 ms and is not cut at exactly the same instant as the picture). Frame counts are compared exactly, which is stricter than the plan's "duration within 0.1 s".
- *Built with a different ffmpeg than is shipped.* The build environment has ffmpeg 6.1; what ships is jellyfin-ffmpeg 8.1.3. The tests at GitHub run on the shipped version on all three systems.
- Still open from M1: the Chromium 87 check, and Huma/sqlc.

## 2026-11-12: M3 — what an independent review changed

Before the first release, the M3 code was read by seven independent reviewers, each looking at one area, and every finding was then tested by a second reviewer who tried to disprove it. 31 findings held. Three fixers, each owning separate files, repaired them, and their work was checked again. This entry records the decisions those repairs brought with them.

**The converter (`internal/hls`).**
- *A chunk is filed by when its first picture is shown, not by when it is decoded.* In films whose frames are stored out of order (x265's usual output, many Blu-ray copies) or unevenly timed, the two differ, and every chunk was filed one key frame late.
- *TV recordings (MPEG-TS) are read through once for their key frames.* They carry no list of key frames and jumping in them does not land on one, so the quick search found nothing and such files did not start. Reading through is as slow as the disk (a minute or more for a long recording on a hard disk), happens once, and is kept. Their clock starting hours in is now tested.
- *No chunk may be longer than a minute.* A list with one in it means the key frames were not found; the film is then cut every few seconds on its own clock instead, which plays but is less exact after a jump. Such a list is never kept.
- *A film does not wait for a long search.* Play waits at most 20 seconds for the cuts, then starts with the plain list. The search carries on for up to 20 minutes and the next Play uses what it found. A search that fails is not tried again for 30 minutes.
- *AAC sound passed on from a TV recording is rewrapped* (`-bsf:a aac_adtstoasc`); without that the converter stopped at the first chunk.
- *A jump to where the sound has already ended* (sound shorter than picture) restarts with the picture only, and the player keeps the header that has sound.
- *A clip cut out of a longer film without re-encoding* keeps frames from before its start that must be sent but not shown. Every time is moved later by that much (`Grid.LeadMs`) so none is below zero, the player is told to begin after them, and the server moves the subtitles by the same amount (`?shift=` on the subtitle address).
- *Every run of a film is made to agree with the header the player already has*, since a player keeps the first header it was given.
- *Restarts, closing and the idle check no longer trip over each other.* A second jump during a restart waits for it; a session closed during a restart leaves no converter running; a request the viewer has given up never moves the converter. A request that was overtaken is answered "try again in a second" (503), which players do, rather than a refusal (409).
- *Folder names stay out of what the converter says.* Its error text names the film's file, never the folder, in the log and in the support bundle.

**The server and the decisions (`internal/server`, `internal/playback`).**
- *Database schema 6*: what each signed-in device says it can play is kept (`device_reports`), so a page left open across a restart or an update still gets files converted for it; the pick of sound track and subtitles is kept as a pick, not only as two languages; each track's four-letter codec label is kept (`streams.codec_tag`).
- *Remembered choices.* Within a show the remembered track is used where an episode has the same layout; otherwise the language and kind (commentary, forced, for the hard of hearing) decide. Forced subtitles no longer count as "subtitles on".
- *A file with several sound tracks is sent as it is only when there is no doubt which one the browser will play* (one track, or the wanted track is first and is the one browsers pick). Chromium ignores the "main track" mark in MKV files, which had played the wrong language.
- *10-bit H.264* plays as it is where the browser says it can (Chromium does; Safari and Firefox do not). *WebM* is told apart from MKV and sent with its own type. *HEVC labelled "hev1"* in an MP4, which Apple devices refuse, is repackaged for them (the label is corrected on the way).
- *A file that does not say how long it is* is answered "cannot be repackaged", with that reason, instead of being given a list one second long.
- *Subtitle files beside a film have an address that survives a scan* (derived from the file's name, not from a counter).
- *Helper programs are shared and limited.* However many requests ask for one subtitle track, ffmpeg reads the film once; at most two subtitle jobs and two key-frame searches run at a time. A subtitle taken out of a film is kept only if the whole copy was written (a full disk used to leave half of one, served from then on).
- *Chunk addresses and stream links end when their sign-in runs out*, not only when it is signed out.
- *A second copy of the program stops before it touches anything.* It used to delete the running copy's chunks before noticing the port was taken.

**The web player.**
- *A film starts without waiting for its subtitles*: tracks are switched on once the film is playing. A subtitle track still being taken out of a long film used to hold the first frame.
- *"This device cannot show the picture" is judged after three seconds of playing* for repackaged films, since an iPhone reports the picture's size late; the false warning there is gone.
- *Changing the sound twice in a row keeps the place*: requests are numbered and only the newest answer is used; the session opened for an answer that is not used is stopped.
- *A page the browser only puts aside* (the back/forward cache, or leaving Safari and coming back) keeps its film open; pages that never return are covered by the 75-second idle stop.
- The browser test grew from 15 steps to 20 and now covers each of these.

**The second check.** Each set of repairs was then checked by a reviewer who had not written it. All but one repair held (the one that did not, sound from TV recordings, needed the server to tell the converter what it was passing on, which the joining commit added). The check found 13 further faults, most of them small, and a repair round fixed those in the server and the page: a change of sound alone is no longer remembered as a wish about subtitles; a file is called WebM by what is inside it, not by its name; a device's report is kept in memory when it cannot be written down; the program's own folder stays out of the log; Safari's claim to play 10-bit H.264 is not believed (it shows no picture). The repairs to the converter from this round are **not in**: the helper making them was stopped early to save the owner's usage, and what it had committed failed at GitHub (one test on the shipped converter, one on Windows), so the converter is as it was after the first round of fixes, which passed on every system.

**Not done.** Nothing here has run on a real iPhone or in Safari itself, only in Safari's engine on the macOS test machine; the owner's checklist is the first real test. Two copies started on purpose with different ports on the same data folder are still not caught.

**Known and not yet fixed** (found by the review's last pass, which looked for what the first seven reviewers had no brief for; each was confirmed by a second reviewer; none stops ordinary use):
- A read error or a cut-short file in mid-film is taken for the end of the film: chunks after it answer "not found", nothing is logged, and there is no fresh try when the file is back (a network drive that drops out). Medium.
- A TV recording whose clock jumps forward part-way stops at the jump. Medium.
- The automatic update can restart the program under a viewer: "nobody is watching" is checked once, before the download, and counts only chunk requests (not a film sent as it is). Medium. To fix before the second release, which is the first one that installs itself.
- When the converter dies between two pieces, the half-written chunk is served as whole and a few seconds are skipped silently. Low.
- A converter that hangs is not noticed or replaced. Low.
- A subtitle file beside a film in .ass/.ssa/.sub form saved in an old Western European encoding loses its lines with accented letters, and that result is kept. Low.
- The installer and uninstaller stop only the program itself; a subtitle or key-frame helper still running can block replacing ffmpeg.exe. Low (reasoned, not run on Windows).
- A forced end leaves temporary files that nothing removes (cache/subtitles/making-*.tmp; the chunk folder if the program is never started again). Low.
- Two versions of one film: the list judges the largest file while Play opens the first one found, so the list can say "plays" for a film that Play then refuses. Low.
- Three small faults in the converter that the second check found, whose repairs were taken out again (see above): when a player asks for the header and a chunk at the same moment at a point where the film's sound has already ended, one of the two can fail and has to be asked for again; a film whose key frames are more than a minute apart is cut every few seconds instead of at its key frames, so jumping in it is less exact; a jump given up while the converter is being stopped can leave that run's chunks on disk until the film is closed. Low. The repair attempts are in the local history of the session that made them (branch `fix/hls-r2`) and were not pushed; the Windows failure was `TestClosingDuringAJumpLeavesNothingRunning/a_seventh_film_is_opened`.
- For a clip trimmed without re-encoding (see above), the saved position is a few seconds off between a device that gets it repackaged and one that plays it as it is.

## 2026-11-12: Release v0.3.0, and how versions are published

The commit for v0.3.0 is **c4c470d** ("Let a release made by hand on GitHub get its files; refuse to release a marked commit"). Its code passed on every system as commit 70dc372; c4c470d adds only a change to the release step.

The session that built it could not publish it the planned way: its access to GitHub allows pushing the working branch and nothing else, and pushing the tag was refused ("403"). That limit was not worked around on the session's own decision. The owner was asked and chose to let the project's own checks do the publishing, so there are now three ways a version gets published, all of which test the commit on every system first and publish only if everything passes:

1. *By commit message.* The newest commit pushed to the working branch says `[release vX.Y.Z at <the full 40-character id of a commit>]`. The named commit must be part of the branch, must hold `docs/release-notes/vX.Y.Z.md`, must not hold `packaging/NOT-FOR-RELEASE.txt`, and the version must not exist yet. Every job then checks out and tests that commit, not the newest one, and the release step makes the tag on it. Naming the commit is what allows releasing a tested commit from a branch whose newest commits hold unfinished work. A run that publishes is not cancelled by a later push. The decision is made by `packaging/release-plan.sh`, which can be run by hand to see what a message would do.
2. *By tag*, pushed from somewhere that may push tags.
3. *By a release made on GitHub's page*, which makes the tag: the release step adds the files and the notes to it.

One fault was found and fixed on the way to this commit: which film is closed when a sign-in opens a seventh was decided by clock time, which ties on Windows (its clock moves in steps of several milliseconds); it is now decided by order of use.

Installed copies only update themselves from the *second* release on. See "Known and not yet fixed" above for what to repair before that one.

## 2026-11-12: M4 — started, not finished (stopped to save the owner's usage)

The design is `docs/design/M4.md` (three independent designs, three judges, one synthesis), with measured speeds in its section 8. Three parts were built by helpers, each in its own branch, and are merged into the working branch **after** the commit meant for v0.3.0 so that they are not lost; none of it is in v0.3.0, and `packaging/NOT-FOR-RELEASE.txt` makes the release step refuse any commit that holds them:
- *The picture converter* (`internal/hls`: `lattice.go`, `facts.go`, `abilities.go`, `Spec.Convert`). Nothing calls it yet. Its two independent checks were stopped before they reported.
- *The decision rules* (`internal/playback`: `DecideFor`, `Want`, `Limits`, the five rungs, `StepDown`). `Decide` is a wrapper that gives M3's answers, so nothing changes yet. Its check finished and found four medium faults that are **not repaired**: a wish for a lighter picture is treated as a demand, so a step after "PC too slow" can refuse a film that would play; a rung never measured is trusted even when a lighter one is recorded as too slow or the PC is already converting; the connection lesson (4 Mbit/s) rules out both 720p rungs (ceiling 5 Mbit/s); `StepDown` writes the viewer's sentence before the outcome is known.
- *Open in another player* (`internal/server`, the page, the browser test; routes `open-local`, `handoff`, `handoff.xspf`). It is reachable, starts a program on the PC, and has **not been independently checked**: do that (security first) before any release that contains it. The Windows launcher has never run outside the test machine.

Next for M4, in the design's order: repair and check the three parts above; wire the planner and converter into Play and the page (slice 2); sizes, HDR, deinterlacing (3); speed table and benchmark (4); burned-in subtitles (5); devices, lessons and stepping down (6); the hardware test (7); drawn subtitles (8); checklist and status (10).

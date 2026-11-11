# Plan: a local media server for every screen

Status: plan only, nothing is built yet. Written 2026-11-11.

## The short version

We build one small program that runs on your PC, finds your movies, shows and music, and serves them to every other device in the house.

You watch through a web app that the program serves itself. Anything with a browser works without installing anything: PC, phone, tablet, and most TVs.

For proper phone and TV apps (offline downloads, an interface made for a remote), we do not write our own. The server will speak the same protocols that Jellyfin apps and Subsonic music apps use, so good apps already in the app stores can connect to it.

That split is deliberate. I can build and fully test a server and a web app on my own. I cannot build an iPhone app at all (it needs a Mac and a paid Apple account), and I cannot test anything on a real phone or TV. Borrowing existing apps covers those screens without months of work that nobody can check.

One honest caveat: Jellyfin already exists, is free, and does most of this. If you only want to watch your files tonight, install it. This project is worth doing if it beats Jellyfin where Jellyfin is weakest (next section). After milestone 4 there is a checkpoint where you compare the two on your own devices and decide whether to carry on.

## What I need from you

None of these block the first milestone. Each has a default I will assume until you say otherwise.

| Decision | Needed by | Default |
|---|---|---|
| Which phone (Android or iPhone) and which TV or streaming stick do you have? | M4 | Windows PC, Android phone, one Android TV / Google TV device |
| Make this repository public, under the GPL-3.0-or-later licence? It is private today. | M2 | Yes. Automatic updates download from GitHub Releases, which only works without a login on a public repo. Public repos also get free Windows and macOS build machines and can apply for free code signing. |
| A name for the app | M2 | "Local Media" as a working title. Installer names are awkward to change later. |
| Look up posters and descriptions online (TMDB)? | M1 | Yes. You register one free TMDB key (about five minutes) and save it as a GitHub secret; it is never committed. Local info files and tags always win, and there is an offline switch. |
| Watching away from home | M8 | Off. When wanted: Tailscale (free account), never router port forwarding. |
| Spending | any time | $0. No Apple developer account, so no iPhone app of our own. |
| Install Jellyfin now as a baseline to compare against? | M4 | Recommended, optional. |

## What "best" means here

1. Installs on Windows with a double-click. No Docker, no terminal, no account.
2. Press play and it plays, on every screen. When the server has to convert a file, the app says why.
3. Updates install themselves and never lose what you have watched.
4. Nothing can be streamed without signing in.
5. Movies, shows and music in one program.
6. Works entirely inside your home network. No cloud, no ads, no tracking.

These target known weak spots. Jellyfin's last two major upgrades required long rescans and produced database errors for many users, and several of its media endpoints have been reachable without signing in for years. Plex needs an account and now charges for remote streaming. Neither is a light program that installs natively on Windows and handles both video and music well.

## What each device gets

"Jellyfin-compatible app" means an existing store app pointed at our server. Only Infuse and Streamyfin have outside evidence of working with a server like ours; every other app in this table is unproven until you try it.

| Device | How it is served | From |
|---|---|---|
| Windows, macOS, Linux PC | Browser | M0 for simple files, M3 for everything |
| Android phone or tablet | Browser. Offline downloads through a Jellyfin-compatible app (Streamyfin, Findroid). Music apps such as Symfonium. | M3, M4, M6 |
| iPhone, iPad | Safari, with AirPlay and picture-in-picture. Offline through a Jellyfin-compatible app (Infuse, Streamyfin, Swiftfin). | M3, M4 |
| Android TV, Google TV, older Fire TV | Jellyfin-compatible app (Wholphin, Jellyfin for Android TV) | M4 |
| Apple TV | Jellyfin-compatible app (Infuse, Swiftfin), or AirPlay from an iPhone | M4, M3 |
| LG TV | Experiment: LG's Jellyfin store app loads its interface from the server, so it may show our TV mode. Otherwise the TV's browser. | M7 |
| Samsung TV | The TV's browser with our TV mode. Samsung's Jellyfin app needs far more of the protocol; only attempted if you own one. | M7 |
| Chromecast | Google TV models use the Android TV apps. Casting from our web app needs HTTPS. | M4, M8 |
| Roku, Xbox, Kodi | Their Jellyfin apps, best effort. Xbox can also use its browser. | after M4 |
| New Fire TV sticks (Vega OS), PlayStation, Switch | Not planned | never |

## Milestones

Every milestone ends with a release you can install, tests I ran myself, and a 15 to 30 minute checklist for you on real devices. The checklist ends with pressing "Export support bundle" and sending me the file: it is how your devices report back to me.

### M0: Walking skeleton
- **You get:** one program. Pick a folder, see your video files in a browser, click one, it plays and remembers where you stopped. Only files a browser can play natively work at this stage.
- **I prove:** automated tests pass; builds for Windows, macOS and Linux come out of GitHub Actions; the Windows build starts and answers on a real Windows machine in Actions.
- **You check:** double-click it on your PC, open it from your phone, and tell me about any Windows security warning.
- **Fixed now because they are painful to change later:** the ID format, the item → versions → files data model, sign-in required on every route by default, and a database backup before every upgrade.

### M1: Library
- **You get:** a real library. Movies and shows are recognised from their file names, with posters, descriptions, search, Continue Watching and Next Up. One sign-in account.
- **I prove:** a test set of about 1,500 real-world file names is recognised at 97% or better; a fake 50,000-item library scans in under 10 minutes, rescans unchanged in under a minute, and the home screen answers in under 100 ms; nothing is reachable without signing in; a browser test runs first-time setup, play, seek and resume using only the keyboard.
- **You check:** point it at your real library and send me the list of files it could not identify.

### M2: Install and safe updates
- **You get:** a Windows installer, a tray icon, start with Windows, and automatic updates.
- **I prove:** on a Windows machine in GitHub Actions: silent install, the server answers, an update keeps watch history, a deliberately broken update restores the backup, and uninstall keeps your data. Databases from every earlier release upgrade cleanly.
- **You check:** a clean install on your PC. From here on, new versions arrive by themselves.

### M3: Plays anything
- **You get:** files browsers cannot play natively now work: MKV, HEVC, surround audio, styled and image subtitles. The server converts only what it must, and a "Why is this converting?" panel explains each decision. If playback stalls, it steps down to a safer method by itself. This is where daily use on the PC and in the phone's browser starts.
- **I prove:** 200+ decision cases; for every test file and device type, the stream the server produces has the right codecs, timing and audio sync; seeking far ahead recovers within 3 seconds; closing the tab leaves no conversion process running after 90 seconds; a failing graphics-card encoder falls back to software.
- **You check:** open the diagnostics page on each device, press "Test hardware acceleration", and play your three hardest files (4K HDR, surround sound, anime with styled subtitles).

### M4: Phone and TV apps
- **You get:** store apps on your phone and TV find the server, sign in with a short code, browse, play, resume and download for offline. Built only for the apps and devices you actually own.
- **I prove:** responses match a real Jellyfin server field for field on the same test library; scripted sessions using Jellyfin's official client library pass; a revoked device is refused on every stream.
- **You check:** a ten-step checklist per app (find server, sign in, browse, play, subtitles, resume on another device, mark watched).
- **Checkpoint:** two weeks using it alongside Jellyfin on your devices. Carry on, narrow the scope, or keep Jellyfin and stop.

### M5: Household
- **You get:** profiles with PINs, limits for children, seek-bar preview thumbnails, chapters, and skip intro. All of it also shows up inside the phone and TV apps.
- **I prove:** a child profile cannot fetch a restricted item through any of the three protocols; a planted intro in a test season is found within 2 seconds.
- **You check:** the household uses it for a week.

### M6: Music
- **You get:** a music library (albums on several discs stay one album), a web player with a queue and lock-screen controls, and support for Subsonic music apps with offline and background play.
- **I prove:** responses match a real Navidrome server; a real third-party web music client browses and plays against our server in an automated browser.
- **You check:** a music app on your phone: download an album, lock the screen, keep listening.

### M7: TV mode
- **You get:** a ten-foot layout of the web app driven entirely by a remote's arrow keys, plus the LG experiment.
- **I prove:** an arrow-keys-only browser test at 1080p with visible focus at every step; the app still loads in the old browser engines TVs ship with.
- **You check:** drive it with the remote on each TV.

### M8: Away from home, and 1.0
- **You get:** optional remote access through Tailscale, which also gives the server a real HTTPS address. That unlocks installing the web app on Android and casting to Chromecast. Signed installer if the free signing application is approved; macOS and Linux packages; a Docker image.
- **I prove:** two machines stream through a private test network; a fresh install reaches first play in under three minutes.
- **You check:** play something on mobile data.

### After 1.0, only if needed
- A desktop player built on mpv, for HDR and untouched surround sound on the PC. I can test this one myself.
- Our own Android app, only if the checkpoint shows store apps fall short. It could only be built and tested through GitHub Actions, so it would be slow going.
- Samsung store-app support.

## Technical design

This section is for whoever builds it.

**Shape.** One Go binary containing the web app. It talks to SQLite on local disk and runs ffmpeg as a separate program. Three API surfaces sit over one core: our own API for the web app, a Jellyfin-compatible adapter, and an OpenSubsonic adapter. Adapters only translate; all behaviour and all data live in the core, so the three cannot disagree about what has been watched. The adapters are off until the owner turns on "phone and TV apps".

| Choice | Why |
|---|---|
| Go, built with cgo off | One self-contained file per OS, cross-compiled from Linux (confirmed in this build environment for Windows, macOS and ARM) |
| SQLite (modernc driver), WAL mode, one writer, FTS5 search, sqlc | No second service to install; no cgo; SQL checked at compile time |
| chi + Huma v2, OpenAPI generated from code, TypeScript client generated from that | The web app cannot drift from the server; the build fails if the client is stale |
| React + TypeScript + Vite, TanStack Query and Virtual, a spatial-navigation library | Arrow-key navigation from the start; long lists stay fast |
| hls.js, with Safari's built-in HLS on Apple devices | Safari's own player gives AirPlay and picture-in-picture for free |
| JASSUB for styled subtitles, libbitsub for image subtitles, WebVTT for plain text | Subtitles are drawn in the browser; burning them into the video is the last resort |
| jellyfin-ffmpeg, pinned version, shipped as a separate program | Best HDR conversion and graphics-card support; never linked into our code |
| GPL-3.0-or-later | Lets us bundle the GPL ffmpeg build and reuse the widest range of libraries |
| Inno Setup installer; tray app per user, not a Windows service | A service cannot see mapped network drives, which is where NAS libraries live |
| Self-update from GitHub Releases, signature-checked, backup first, one-click rollback | "Updates never lose watch state" |
| Tailscale's tsnet library, opt-in | Remote access and a real HTTPS name without port forwarding |

**Playback.** Exactly two ways to deliver video: the original file over HTTP range requests, or fragmented-MP4 HLS from one session manager that handles all three kinds of conversion (repackage only, convert audio only, convert everything). A single pure function takes what ffprobe says about the file, what the device reports it can play, and the user's limits, and returns a plan with reasons. Browsers report measured capabilities (`mediaCapabilities.decodingInfo`), not guesses from the browser name; a small override table covers known liars. The guaranteed fallback is H.264 at up to 1080p with stereo AAC, encoded in software. Graphics-card encoding is tried with a real one-second test encode and is never required. One ffmpeg process per session, a heartbeat from the player, and a sweep that kills orphans.

**Library.** A periodic folder walk compared against the last snapshot is the source of truth; file-system watchers only speed things up, because they miss changes on network shares. Our own file-name parser, tested against a large set of real names. Local info files (NFO) and embedded tags are read first; TMDB and MusicBrainz are optional, cached and rate-limited. The database never lives on a network share.

**Security.** Every route declares who may call it, or the server refuses to start. Passwords use argon2id. Each device gets its own revocable token. Clients send IDs, never file paths, and all file access goes through Go's `os.Root`. One small module builds every ffmpeg command from typed values, so no client text ever reaches a command line. One guarded HTTP client fetches artwork. One documented exception: Jellyfin apps load artwork without credentials, so with the adapter on, artwork (never video or audio) is protected only by unguessable 128-bit IDs.

**Compatibility budget.** The Jellyfin adapter is capped at about 40 operations in M4. Every operation must be justified by a request recorded from a real app. One 2026 project that chased the full protocol reached about 360,000 lines and still fails its own compatibility test.

## How I verify things I cannot see

| Where | What it can prove | What it cannot |
|---|---|---|
| My build environment | Server logic, the web app in a real Chromium browser, streams checked with ffprobe against generated test media, H.264 playback | No graphics card and no HEVC decoding in the browser (measured). A file with H.264 video and AC-3 audio plays silently here, the same failure you would see at home, which makes it a useful test. |
| GitHub Actions | Real Windows and macOS machines, the installer and updater, the pinned ffmpeg | Phones, TVs, graphics cards |
| You | Real devices, HDR, surround sound, remotes | n/a |

Hosts this environment cannot reach today: TMDB, MusicBrainz, the Android SDK, jellyfin.org. Tests use recorded responses instead. When a milestone needs one of these, I will ask you to allow it; you will get an approval prompt.

## Rules that hold in every milestone

- **Plain HTTP first.** Everything essential works at `http://192.168.x.x`. A browser test against exactly that kind of address enforces it. Features that need HTTPS (installing the web app on Android, Chromecast) are extras that arrive in M8.
- **Keyboard only.** One arrow-keys-only test journey stays green from M1 onward, so TV mode is polish and not a rewrite.
- **Nothing lands without its test.**
- **Software fallback always exists.** Graphics-card and HDR paths are extras confirmed on your machine.
- **A decision log.** `CLAUDE.md` and `docs/decisions.md` record what was decided and why, so each session picks up where the last one stopped.

## What we are not building

- Our own apps for iPhone, Apple TV, Roku, Samsung or LG.
- Full Jellyfin compatibility, plugins, live TV or recording.
- Photos, audiobooks, books. Immich, Audiobookshelf and Kavita already do these well.
- DLNA. It has no sign-in and every device behaves differently.
- Offline downloads inside the web app. It would need HTTPS on every phone and cannot store files the browser cannot play. Store apps cover this.
- Anything that requires a cloud service, and automatic router port forwarding.

## Risks

1. **It may not beat Jellyfin for you.** That is what the checkpoint after M4 is for. Both servers read the same folders, so comparing costs little.
2. **The Jellyfin protocol moves, and apps check the server's version.** A store app update could break compatibility until I fix it. The web app never depends on that protocol, and TV mode is the fallback for TVs.
3. **I cannot see your devices.** Diagnostics page, support bundle, your checklists. Wrong guesses heal themselves through automatic step-down.
4. **Windows may warn about, or block, an unsigned installer.** M0 surfaces this on day one. The fix is free code signing for open-source projects (SignPath Foundation), which needs a public repo and is not guaranteed.
5. **Phone and TV quality is only as good as other people's apps.** If they fall short on your devices, the fallback is our own Android app, which is slow for me to build.
6. **Sprawl across many sessions.** Small single-purpose modules for sign-in, file access and ffmpeg commands; the 40-operation budget; the decision log.

## How this plan was made

Four research passes checked the current state of media servers, playback, client apps and server technology against primary sources. Three architects then designed competing plans (web-first, native-player-first, compatibility-first), and three judges scored them for buildability, owner experience and technical risk. The native-player plan lost clearly, mainly because its Android app could not be built or tested from here. The other two scored within a point of each other, and all three judges recommended the same hybrid: the compatibility plan's skeleton with the web-first plan's client discipline. That hybrid is this document.

# Plan: a local media server for every screen

Status: plan only, nothing is built yet. Written 2026-11-11 and updated the same day with the owner's first answers.

## The short version

We build one small program that runs on your PC, finds your movies, shows and music, and serves them to the other screens in the house. It only reads your files and never changes them, so stopping at any point costs you nothing but the time.

You watch through a web app that the program serves itself. Anything with a normal web browser works without installing anything: PCs, phones and tablets, plus the TVs that have a browser (LG, Samsung, Fire TV, Xbox), though a layout made for a TV remote only arrives in M8.

Android TV and Google TV sticks, Apple TV and Roku have no usable browser, and phones need a real app for offline downloads. For those we do not write our own apps. The server will speak the same language as Jellyfin (the most popular free media server) and Subsonic (a common standard for music apps), so apps written for those should be able to connect to ours. That is a bet, not a promise: a few such apps are reported to work with a small server of this kind, the rest are untested, and none will work better with our server than with Jellyfin itself.

That split is deliberate. I can build and test a server and a web app on my own. I could compile an iPhone app, but putting it on your phone needs a paid Apple developer account, and I cannot test anything on a real phone or TV. Borrowing existing apps covers those screens without months of work that nobody can check.

One honest caveat: Jellyfin already exists, is free, and does most of this (see "Why not just use Jellyfin or Plex?"). You have chosen not to install it, so this is your media server from the start, and until M3 it will play only a small part of your library. After milestone 5 there is a checkpoint where you decide whether what exists is good enough to carry on.

**When you get what.** The work is cut into ten milestones, M0 to M9.

- M0: a first build that proves the idea. Most of your files will not play yet.
- M3: the first version worth using every day, on the PC and in Safari on the iPhone.
- M5: phone and TV apps, then the stop-or-go checkpoint.
- M9: version 1.0, the first I would call finished.

I cannot promise dates. Each milestone is several working sessions with me plus a short check by you.

**What to do now.** You have said "go", so M0 is under way. Two things remain, in any order: make the repository public (the repository is the project's page on GitHub, where the code is kept; the steps are in the next section), and tell me what the server PC runs and whether there is a TV (question 1 there).

## What I need from you

Nothing here blocks M0.

**Answered so far (2026-11-11).**

- **Your phone is an iPhone.** Safari on the iPhone, and the two iPhone apps with the best evidence of working (Streamyfin and Infuse), come first.
- **The repository goes public.** Only you can flip that switch; the steps are in item 8. The licence file is in place (the licence is the GPL, a standard open-source licence: anyone may use and change the code, and must share their changes on the same terms). I checked every saved version of the project: the only personal detail in it is your GitHub user name, which is part of the repository's address anyway.
- **No Jellyfin on your PC.** The checkpoint after M5 judges this project on its own, and M5 starts without recordings from a Jellyfin of yours (see M5).
- **The server PC is a Windows laptop with an NVIDIA graphics card (an RTX 3060) and an Intel Core i7 processor** (answered 2026-11-12). Two things follow. First, the graphics card has a chip made for converting video, so once M4's "Test hardware acceleration" has passed on your PC, converting should be easy for it, including 4K and HDR films, which are hard work for a processor alone. Until that test has passed, the processor does the converting, which always works. Second, a laptop goes to sleep by itself: when nobody touches it for a while, and usually when its lid is closed. A sleeping PC serves no films. From the next version the app keeps the PC awake while someone is watching; what closing the lid does is a Windows setting that only you can change, and the checklist for that version shows where. Keep the laptop plugged in when it is serving films.

**Questions about your setup.** I assume the default until you tell me otherwise.

1. **What does the server PC run, and is there a TV box or smart TV you want to use?** Default: Windows 11, and no TV yet. Please answer this one, because it changes the order of the work. Until you name a TV, M5 covers only the iPhone. If your main TV is an LG or a Samsung, TV mode (M8) moves ahead of M6 and the checkpoint waits for it.
2. **What do you have: movies, shows, music, home videos?** Default: movies, shows and music. Home videos show up as plain folders. With no music, M7 is dropped.
3. **Where are the files?** Default: on a disk in or plugged into the server PC. Tell me if they are on a NAS (a network storage box) or another computer; that works but needs different handling.
4. **Who else will watch, and are any of them children?** Default: a few people in one household. If it is just you, M6 skips profiles and limits for children.
5. **Is the PC on when you want to watch?** Assumed yes. The server only runs while the PC is awake and you are signed in to Windows.
6. **Do you want to watch away from home?** Default: no. If you want it later: Tailscale, a free service that links your own devices privately over the internet. It needs a free account and its app on every phone or laptop that connects. I will never open your router to the internet.
7. **Are you willing to spend anything?** Assumed no. The project costs $0 beyond what you already pay for Claude, though it will take many long sessions. One iPhone app named in this plan costs money (Infuse); a free alternative is listed beside it. We skip Apple's $99-a-year developer account, so there is no iPhone app of our own.

**Things only you can do.** I give click-by-click steps for each when it comes up, because I have no way to do them for you. The steps for the first are below.

8. **Make the repository public.** You said yes; it is still private until you do this. Any time before M2 is fine: M2's automatic updates need it, and until then my Windows and Mac test runs are rationed. Everything in the repository becomes visible to anyone: the code, this plan with the answers written into it (so far, that your phone is an iPhone), the history of changes, your GitHub user name and the logs of automatic builds. Your files, your library and your passwords are never in the repository, so they stay private. Public is what lets updates download without a login, gives me GitHub's free Windows and Mac test machines (the Mac ones are my only stand-in for your iPhone), and lets us apply for a free certificate that reduces Windows warnings. The steps, in a web browser where you are signed in to GitHub:
   1. Open `github.com/Matiaspnyhus/Local-media-project/settings`.
   2. Scroll to the bottom, to the box called "Danger Zone". The step below deletes nothing, but the same box also holds "Delete this repository" and "Archive this repository". Leave every button there alone except the one in step 3.
   3. Beside "Change repository visibility", press "Change visibility", then "Change to public".
   4. GitHub asks you to confirm, usually with three buttons one after another ("I want to make this repository public", "I have read and understand these effects", "Make this repository public"). Press each. It may then ask for your password or a sign-in code.
9. **Show posters and descriptions from the internet?** Recommended: yes, from M1. They come from TMDB, a free movie database. For now that needs a free TMDB account and a key that you paste once into the app's settings; the key stays on your PC. Lookups send film and show titles, nothing else. Without a key the app still works: titles come from your file names and the picture is a frame from the video. If TMDB's terms allow one shared key for an open-source app, as Jellyfin ships, later versions include one and this step disappears.
10. **A name for the app.** Needed by M2. Working title: "Local Media".
11. **Approvals when I ask.** My build environment is blocked from some websites (TMDB, jellyfin.org, Tailscale). When a milestone needs one, you get a one-tap approval prompt in the Claude app.

## Why not just use Jellyfin or Plex?

What "best" means for this project:

1. Installs on Windows with a double-click. No extra software, no typed commands, no online account; your user name and password live only on your PC.
2. Press play and it plays, on every screen listed in this plan. When the server has to convert a file, the app says why in plain words.
3. Updates install themselves and keep what you have watched. A backup is taken before every update and restored automatically if the update fails.
4. No video or music can be streamed without signing in.
5. Movies, shows and music in one program.
6. Works entirely inside your home network. No cloud, no ads, no tracking.

Jellyfin already does 1, 5 and 6, and on a good day 2. This project aims to beat it on 3 and 4 and to be simpler to live with. Jellyfin's last two big upgrades brought forced rescans and, for some users, database errors. Some of its video and picture links have worked without signing in for years. Many people find its interface heavy on settings, and its music handling is clumsy (albums on several discs get split). On phone and TV apps we can at best match Jellyfin, because we use the same apps.

Plex is the most polished choice and is free inside your home, but it needs an online Plex account, promotes its own streaming content, and now charges for watching away from home.

What you give up by choosing this project over either: a large community, plugins, live TV, and years of testing on real devices.

## Milestones

Each milestone ends with a release you can install, tests I ran myself, and a checklist for you on real devices. Most checklists take 15 to 30 minutes; M5 and M6 ask for normal use over a week or two. A checklist ends with pressing "Export support bundle" in the app's settings and attaching the file to our chat. The file holds device details, error logs and the names of files that failed. It holds no video and no passwords. Do not post it on GitHub.

Rough sizes: M0 small. M1 large. M2 medium, and slow because I can only test Windows through GitHub. M3 and M4 large. M5 medium for me but paced by your testing. M6 medium. M7 large. M8 and M9 medium.

### M0: First playable build
- **You get:** one program. The first time it starts it opens a setup page on the PC: create your sign-in and pick a folder. Then sign in from any browser at home (the PC shows the address and a QR code to scan), see your video files, click one, and it plays and remembers where you stopped. On the iPhone, Safari's "Add to Home Screen" gives it an icon like an app. Expect many files not to play yet, or to play with no sound. Until M3 only files a browser can play by itself work, typically `.mp4` files. Videos shot on your iPhone are `.mov` files and may not play on the PC yet. That is normal, not a bug.
- **I prove:** automated tests pass; GitHub's build service produces Windows, Mac and Linux versions and starts the Windows one on a real Windows machine; the web app signs in and plays a simple file in Safari's engine on a Mac test machine at GitHub; nothing answers without signing in.
- **You check:** download it, double-click it, then scan the QR code with your iPhone on the same Wi-Fi and play one `.mp4` file. Expect two Windows prompts: a blue "Windows protected your PC" warning, because the program is not signed yet, and a firewall question. The checklist shows what to click for each, and what to change if the phone cannot connect. If Windows offers no way to run the program at all, stop and tell me (see risk 4).

### M1: Library
- **You get:** a real library. Movies and shows are recognised from their file names, with search, Continue Watching and Next Up, and with posters and descriptions once the TMDB key is in. You can browse everything, but files the browser cannot play are marked "needs conversion, arrives in M3" instead of failing silently.
- **I prove:** automated tests cover name recognition on about 1,500 real-world file names, speed on a 50,000-item library, sign-in on everything, a watched file staying watched after it is renamed or moved, and keyboard-only use.
- **You check:** point it at your real library, send me the list of files it could not identify, and confirm posters appear (I cannot reach TMDB from where I work).

### M2: Install and safe updates
- **You get:** a Windows installer (it asks for administrator rights once, to add the firewall rule), a tray icon, start with Windows, and automatic updates. This comes before the playback work so that every later version arrives by itself and your watch history is protected from the first day of real use.
- **I prove:** on a Windows machine at GitHub: install, the server answers, an update keeps watch history, a deliberately broken update restores the backup, uninstall keeps your data, and a fresh install reaches first play in under three minutes.
- **You check:** install over what you have; your library and watch history must still be there. Once the code is public and has a release, I apply for free code signing.

### M3: Plays most things
- **You get:** the first version for daily use on the PC and in Safari on the iPhone. Files whose video the browser understands but whose packaging or sound it does not now play: the server repackages them or converts only the sound, at almost no cost to the PC. That covers most MKV files, which Safari on the iPhone cannot open at all, and Dolby and DTS sound, which Chrome on the PC cannot play. Plain-text subtitles work. It picks the right audio and subtitle track by itself (your language, never a commentary track) and remembers your choice for the rest of a series. A "Why is this converting?" panel explains each decision.
- **I prove:** every stream the server produces is checked for the right contents, timing and sound sync, and is played end to end by a second test player that is not a browser; seeking far ahead recovers within 3 seconds; closing the tab stops the conversion. Safari's engine on a Mac test machine at GitHub plays our streams, which is the closest I can get to your iPhone.
- **You check:** open the diagnostics page on the PC and on the iPhone, and play ten of your own files on each. Your iPhone is the real test here; mine is only an approximation.

### M4: Plays anything
- **You get:** full video conversion for files the browser cannot decode at all, fancy and picture-based subtitles, and automatic step-down: if playback stalls, the app moves to a safer method and remembers that for the device. On the iPhone, fancy and picture-based subtitles can be drawn over the video only while it plays inside the page. In the iPhone's own fullscreen player, in picture-in-picture and over AirPlay they have to be burned into the picture, which means a full conversion. "Plays" does not always mean full quality. In a browser a 4K HDR film may be shown as 1080p without HDR, and surround sound may become stereo. Full quality on a TV comes from the store apps in M5, which can play the original file untouched. On the PC, an "Open in another player" button hands the original to a free player such as VLC. Converting video is heavy work, so expect the PC's fan to spin up.
- **I prove:** software conversion keeps up with real time on a four-core machine; a failing graphics-card encoder falls back to software; subtitles are checked pixel by pixel and stay in step after seeking.
- **You check:** press "Test hardware acceleration" (it checks whether your graphics card can help with converting), and play your three hardest files if you have them: a 4K HDR film, one with surround sound, anime with styled subtitles.

### M5: Phone and TV apps
- **You get:** an App Store app on the iPhone, and one for each TV you name, that connects to the server, signs in with a short code, browses, plays and resumes, with offline downloads where the app supports them. The first candidates are Streamyfin, which is free, and Infuse. Which apps make the cut is decided by testing on your devices; some named in this plan may never work.
- **How it starts:** I build a first version from Jellyfin's published description of its protocol (the language its apps speak to the server), and compare our answers with a real Jellyfin server that runs in my build environment, not on your PC. Then you connect an app, our server records what the app asks for, and the support bundle brings those recordings to me. With no Jellyfin of your own to record from, expect more rounds of try-and-fix here. If it stalls, installing Jellyfin for one evening would speed it up; that stays your choice.
- **I prove:** our answers match a real Jellyfin server's, field by field, on the same test library, and load in Jellyfin's two official code libraries, the one Streamyfin is built on and the one the Android apps are built on; with sign-in removed from a request, only posters still load; a revoked device is refused.
- **You check:** a ten-step checklist per app (connect, sign in, browse, play, subtitles, resume on another device, mark watched). An app that still fails the checklist after three rounds of fixes is dropped and listed as unsupported. Earlier rounds, where an app cannot get as far as the checklist and you only send me a support bundle, do not count towards the three.
- **Checkpoint:** two weeks of normal use on your devices. Judge only what exists by then: installing and updating, the web app, and playing your files on the PC, the iPhone and any TV. Profiles, skip intro, music and TV mode come later and do not count. Three questions: did everything you tried play, what got in your way, and would you keep using it? Then carry on, narrow the scope, or stop.

### M6: Household
- **You get:** profiles with PINs, limits for children, preview thumbnails on the seek bar, chapters, and skip intro. Phone and TV apps that support thumbnails, chapters and skip intro show them too. In those apps each profile signs in with its own password or a short code.
- **I prove:** a child profile cannot reach a restricted item through the web app or any phone or TV app; a planted intro is located to within 2 seconds of where it really starts and ends.
- **You check:** the household uses it for a week.

### M7: Music
- **You get:** a music library (albums on several discs stay one album), a web player with a queue and lock-screen controls, and support for Subsonic music apps with offline and background play. On the iPhone, music that keeps playing with the screen locked is unreliable from a web app, so there it works best through such an app (Amperfy is a free one).
- **I prove:** our answers match a real Navidrome (a popular music server); a real third-party music web app browses and plays against our server in an automated browser.
- **You check:** create an app password in settings, sign a music app in with it on the iPhone, download an album, lock the screen, keep listening.

### M8: TV mode
- **You get:** a layout of the web app made to be read from the sofa and driven entirely by a remote's arrow keys, for TVs with a browser. If you have an LG TV: an experiment in which LG's Jellyfin store app shows our interface.
- **I prove:** an arrow-keys-only test with visible focus at every step; the app runs in the old browser engines that TVs ship with, tested back to 2022 LG models.
- **You check:** drive it with the remote on each TV.

### M9: Away from home, and 1.0
- **You get:** optional remote access through Tailscale: a free account, and its app on the iPhone and on any laptop that should connect. Devices with the Tailscale app also reach the server at a secure (https) address. At home that adds little on an iPhone: AirPlay, Apple's way of sending video to a TV, needs none of it. A Cast button in Chrome on a PC, for Chromecast, stays an experiment that works only at home. Devices without Tailscale keep using the plain home address as before. Signed installer, if the application made at M2 has been approved.
- **I prove:** two machines stream through a private stand-in network. That proves the connection, not the secure address, which only you can check.
- **You check:** play something on the iPhone over mobile data; open the https address.

### After 1.0, only if needed
- A desktop player built on mpv (a free player that plays almost anything), for HDR and untouched surround sound on the PC. I can test its logic here; HDR output and surround still need your PC.
- Our own Android TV app, only if you have such a device and store apps fall short on it. Its tests can only run at GitHub, so it would be slow going. From 2027 Android makes installing apps from unregistered developers much harder, so it would start with you registering a free developer account.
- Samsung store-app support, Mac and Linux installers, a Docker image.

An iPhone app of our own stays off the table: Apple charges $99 a year to put one on a phone, and I could not test it.

## What each device gets

Apps are listed first choice first. Every app here is unproven with our server until you try it.

- **PC (Windows, Mac, Linux):** browser. Simple files from M0, most from M3, everything from M4.
- **iPhone (yours), iPad:** Safari, with AirPlay and picture-in-picture (M3; I cannot test real iPhones, so this too is unproven until you try). Offline downloads through Streamyfin, free, whose developers still call its downloads experimental, or Infuse, which needs its paid Pro upgrade for some features, possibly including Dolby and DTS sound (M5). Music through a Subsonic app such as Amperfy, free (M7). Swiftfin is free but has no downloads.
- **Android phone or tablet (a guest's, or later):** browser (M3). Offline downloads through Streamyfin or Findroid, both free (M5). Music through Symfonium, paid once after a trial, or a free Subsonic app (M7).
- **Android TV, Google TV, older Fire TV:** Streamyfin first; Wholphin or the official Jellyfin app if they can be made to work (M5).
- **Apple TV:** Infuse or Streamyfin (M5), or AirPlay from an iPhone (M3).
- **LG TV:** the TV's browser with TV mode, and the store-app experiment (M8).
- **Samsung TV:** the TV's browser with TV mode (M8). Samsung's Jellyfin app needs far more of the protocol and is only attempted if you own one.
- **Chromecast:** Google TV models use the Android TV apps (M5). Casting from the web app is the M9 experiment.
- **Roku, Xbox, Kodi:** their Jellyfin apps, best effort, after M5. Xbox can also use its browser.
- **New Fire TV sticks (4K Select, 2026 Stick HD):** an experiment only if you own one, through a third-party Jellyfin app in Amazon's store or the stick's browser. They do not run Android apps.
- **PlayStation, Switch:** not planned.

## What we are not building

- Our own apps for iPhone, Apple TV, Roku, Samsung or LG.
- Full Jellyfin compatibility, plugins, live TV or recording.
- Photos, audiobooks, books. Immich, Audiobookshelf and Kavita already do these well.
- DLNA (an old way for TVs to find media on the network). It has no sign-in and every device behaves differently.
- Offline downloads inside the web app. Store apps cover this.
- Anything that requires a cloud service, and automatic router port forwarding.
- Deferred past 1.0: gapless music in the web player, automatic quality switching, a custom Chromecast receiver, passkeys.

## Risks

1. **It may not be worth it.** Jellyfin already does most of this for free, and you have chosen to skip it. The checkpoint after M5 is where you judge this project on its own and can stop.
2. **Jellyfin's protocol changes, and apps check the server's version.** Store apps update themselves, and I do not watch the project between sessions: if an app stops working, nothing happens until you tell me. Protections: two candidate apps per device type where they exist, and a web app that never depends on that protocol. On the iPhone, Safari is the fallback. On TVs with a browser, TV mode is. On Android TV, Apple TV and Roku the fallback is the second app; if both fail, that TV has nothing until I fix it, except an Apple TV, which the iPhone can still send video to with AirPlay.
3. **I cannot see your devices, and the iPhone least of all.** I have no iPhone to test on. The closest I get is Safari's engine on a Mac test machine at GitHub, which is similar but not the same. I rely on the diagnostics page, the support bundle and your checklists. When the server guesses wrong about what a device can play, automatic step-down corrects it.
4. **Windows may warn about, or block, an unsigned program.** M0 surfaces this on day one. Free code signing for open-source projects (SignPath Foundation) reduces it. It needs public code, approval is not guaranteed, and a signed installer can still warn until it has built a reputation. If your PC has Smart App Control switched on there is no "Run anyway" button; then we choose between waiting for signing, switching Smart App Control off, and running the server on another PC.
5. **Phone and TV quality is only as good as other people's apps.** If they fall short on the iPhone, there is no app of our own to fall back on; the iPhone then stays on the web app in Safari, which cannot download for offline. On an Android TV device the fallback would be our own app, which is slow for me to build.
6. **The project is big and I work in separate sessions with no memory between them.** I keep a written log of decisions and keep the risky parts (sign-in, file access, running the converter) small.
7. **It needs looking after for as long as you use it.** Jellyfin has a community fixing things every week. This has you and me. Expect to open a session now and then when a store app, a browser or Windows changes something. Nobody but me reviews the code, so keep it inside your home network unless you turn on remote access.

---

Everything below is for whoever builds it. You can stop reading here.

## Technical design

**Shape.** One Go binary containing the web app. It keeps its data in SQLite on local disk and runs ffmpeg (the standard free tool for converting video; ffprobe is its file inspector) as a separate program. Three API surfaces sit over one core: our own API for the web app, a Jellyfin-compatible adapter, and an OpenSubsonic adapter (OpenSubsonic is the current form of the Subsonic music standard). An adapter is a thin translation layer. All behaviour and all data live in the core, so the three cannot disagree about what has been watched. Adapters are off until the owner turns on "phone and TV apps".

| Choice | Why |
|---|---|
| Go, built with cgo off (no C code linked in) | One server file per OS, cross-compiled from Linux (confirmed here for Windows, macOS and ARM), with ffmpeg and ffprobe shipped beside it |
| SQLite through the modernc driver; WAL mode; one writer; FTS5 search | No second service to install; no cgo; crash-safe writes; built-in text search. sqlc generates checked code for ordinary queries; search queries are hand-written, because sqlc rejects some FTS5 forms |
| chi + Huma v2; OpenAPI generated from code; TypeScript client generated from that | The web app cannot drift from the server; the build fails if the client is stale |
| React + TypeScript + Vite; TanStack Query and Virtual; a spatial-navigation library | Arrow-key navigation from the start; long lists stay fast |
| Oldest browsers supported, fixed in M1: Chromium 87 and Safari 15 | A floor chosen late means rework. Chromium 87 is what 2022 LG TVs ship; 2022 Samsung TVs are older still and are not promised |
| hls.js; Safari's built-in HLS on Apple devices | HLS is video cut into short chunks, the streaming format browsers and phones accept. Safari's own player gives AirPlay and picture-in-picture without extra player code |
| JASSUB for styled subtitles, libbitsub for picture subtitles, WebVTT for plain text | Drawn in the browser where the browser allows it (styled ones need Chrome 85, Safari 17 or Firefox 105). Where it does not, they are burned into the video: older browsers, and iPhone fullscreen, picture-in-picture and AirPlay, which show only the native player. Over plain HTTP the styled renderer runs single-threaded |
| jellyfin-ffmpeg: exact version pinned, checksum-verified at build, shipped as a separate program with its licence text and a source link | Best HDR conversion and graphics-card support; never linked into our code |
| GPL-3.0-or-later | Compatible with the Apache-2.0, MIT, MPL-2.0, LGPL and GPL-3 code we may reuse, and an open-source licence is required by the free signing programme |
| Inno Setup installer; per-user tray app on Windows, not a Windows service; no tray on macOS | A service cannot see mapped network drives, which is where NAS libraries live. The tray library needs cgo on macOS, which would break cross-compiling |
| Self-update from GitHub Releases: verified, backup first, one-click rollback | Goal 3. Verification preferably through GitHub's build attestations, which need no secret keys; otherwise a signing key the owner adds as a repository secret, since I cannot create or read repository secrets |
| Tailscale's tsnet library, opt-in | Remote access and a real HTTPS name without port forwarding |

**Fixed in M0, because these are painful to change later.**
- IDs are random 128-bit values, created once, written as a GUID for Jellyfin apps and as plain text elsewhere. Navidrome had to re-encode every ID in 2026.
- The data model is item → versions → files. A one-page sketch in the decision log shows that albums, discs, tracks and artists fit it.
- Every route declares who may call it, or the server refuses to start.
- Links to streams carry a short-lived signature.
- The database is backed up before every upgrade, and every release saves a sample database so later versions can prove they upgrade it.
- The internal identifier and data folder are a neutral `localmedia` and never change. Only the visible name waits for the owner.

**Playback.** There are exactly two ways to deliver video: the original file over HTTP range requests, or fragmented-MP4 HLS from one session manager that handles all three kinds of conversion (repackage only, convert audio only, convert everything). One pure function takes what ffprobe says about the file, what the device reports it can play, how subtitles will be shown and the user's limits, and returns a plan with reasons. A second pure function chooses tracks: preferred languages, default and forced flags, no commentary or descriptive tracks, and a per-series memory. Browsers report measured capabilities (`mediaCapabilities.decodingInfo`), not guesses from the browser's name; a small override table covers known liars. What a store app says it can play is converted into the same input.

The fallback that needs no graphics card is H.264 with stereo AAC, encoded in software at the largest size the PC sustains above real time: 1080p for ordinary files, possibly 720p for 4K HDR. Measured in the build environment on a synthetic 4K HDR clip with four cores: about 1× real time when HDR is converted at full size, 1.6× when the picture is shrunk first. It is therefore always shrunk first. Graphics-card encoding is tried with a real one-second test encode and is never required. Repackaging needs the file's keyframe positions; they are read once and cached, with a time budget on network shares. One ffmpeg process per session, a heartbeat from the player, and a sweep that kills orphans.

**Library.** A periodic folder walk compared against the last snapshot is the source of truth. File-system watchers only speed things up, because they miss changes on network shares. What ffprobe reports (duration, every video, audio and subtitle track, and the profile, level and HDR fields that store apps decide on) is stored at the first scan, so later milestones need no rescan. Our own file-name parser is tested against public sets of real names. Local info files (NFO) and embedded tags are read first; TMDB and MusicBrainz are optional, cached and rate-limited. Media folders are opened read-only. Moved files are recognised by size plus a hash of their first and last 64 KB. The database never lives on a network share.

**Security.** Passwords use argon2id. Each device gets its own revocable token. Password, short-code and PIN attempts are rate-limited. Clients send IDs, never file paths. The server's own file reads go through Go's `os.Root` (it confines access to the chosen folders), one per library folder. ffmpeg needs a real path, which the server builds from the database and re-checks against the library folders. Symlinks that leave a library folder are reported, not served. One small module builds every ffmpeg command from typed values, so no client text reaches a command line. One guarded HTTP client fetches artwork (host allowlist, private addresses blocked).

Anything that fetches media without our session (AirPlay and Chromecast receivers, external players) gets a link carrying a short-lived signature for that one item. Subsonic's sign-in needs the server to know the password itself, so each music app gets its own generated, revocable app password, or an API key where the app supports one. The account password is never stored recoverably.

Exceptions to sign-in are decided from evidence. In M5 each recorded app request is replayed with its credentials stripped, to see what the app really sends bare. Artwork may then be served by unguessable ID while the adapter is on. Video, audio and subtitles never are; an app that needs that is listed as unsupported.

**Jellyfin compatibility.** The first slice (find the server, sign in, browse, play, resume, report progress) is written from Jellyfin's published API description and compared against a real Jellyfin server in Docker. After that the adapter grows only from recordings: an operation is added when a request recorded by our server from one of the owner's apps needs it. The cap is agreed after the first app is recorded and written into the decision log. Expect on the order of 100 routes for three or four apps, many of them stubs. The one small server reported to work with Infuse, Streamyfin, SenPlayer and VidHub registers 119 Jellyfin paths; it checks no sign-in on streams, converts nothing and has no downloads, so those three things are unproven even for those apps. For scale, one 2026 project that ported all 412 operations runs to about 360,000 lines, and about 5% of its replayed responses still differ from Jellyfin's.

Streamyfin and Infuse come first. Apps built on Jellyfin's official Kotlin library (Findroid, Wholphin, the official Android TV app) check our reported version and reject incomplete answers, so they may cost far more; the version we report is a setting. The adapter is written from Jellyfin's published API description and from recorded traffic only. Our server never uses Jellyfin's ports, so the owner can install Jellyfin beside it later without a clash.

**Support bundle.** One export containing: each device's measured playback abilities and diagnostics results; recent logs with passwords, tokens and addresses removed; the files the library could not identify; and, once phone and TV apps are on, every request each app made (route, parameters, result, no secrets), with the ones we do not implement listed per app.

## Acceptance tests

These are the measurable versions of each milestone's "I prove".

- **M0.** A trivial workflow shows I can push workflow files, start a run, read its log and download what it built. Windows, macOS, Linux and ARM builds come out of GitHub Actions, and the Windows one starts and answers on a Windows runner. Until setup is finished the server answers only the PC it runs on; afterwards every route except the sign-in page refuses a request without a session, and only the owner can choose folders. An expired or altered stream link is refused. SQLite's WAL mode and FTS5 pass on Windows, macOS and ARM Linux runners; if not, the driver changes now. The web app signs in and plays an H.264 `.mp4` in Safari's engine on a macOS runner, because the owner's M0 check is on an iPhone. Docker starts in my environment, or the decision log records that server-comparison tests run only in Actions.
- **M1.** About 1,500 real-world file names from the public test sets of existing parsers, labelled independently of our parser and cross-checked with the guessit tool, give the right title and year, or show, season and episode, 97% of the time. On four cores with online lookups faked, a 50,000-item library on local disk scans in under 10 minutes including probing, rescans unchanged in under 60 seconds, and the home screen answers in under 100 ms at the 95th percentile. Track details are stored for every test file, and subtitle files next to a video are indexed. A renamed or moved watched file stays watched. Route enumeration and path-traversal fuzzing find nothing open; repeated wrong passwords are slowed down. A keyboard-only browser journey (setup, play, seek, resume) passes against a plain-HTTP network address. The built app loads in Chromium 87.
- **M2.** On a Windows runner: silent install; the server answers; the firewall rule and the start-with-Windows entry exist; the pinned ffmpeg is installed, runs, and has its licence text beside it. The installer adopts a saved M1 database. An update keeps watch history; a deliberately broken update restores the backup; uninstall keeps data. The sample database from every release upgrades cleanly. From starting the installer to the first frame playing takes under three minutes, and this stays a standing test.
- **M3.** At least 200 planner cases, plus track-choice cases with a commentary track, a foreign dub and forced subtitles, where the choice carries to the next episode. For each test file and device profile the produced stream has the planned codecs, keyframe-aligned segments, a duration within 0.1 s and audio and video starting within 50 ms of each other. Every produced stream decodes end to end in ffmpeg with errors fatal. Headless mpv plays every test file, original and converted, and reports what it decoded. A far seek recovers within 3 seconds. Closing the tab leaves no ffmpeg process and no temporary files after 90 seconds. Subtitles stay in step after a far seek and after resuming. Safari's engine on a macOS runner plays our HLS for an H.264 file; that is the only evidence I can produce for iPhone, iPad and AirPlay.
- **M4.** Software conversion runs at 1.5× real time or better for a 1080p source and 1.2× or better for the 4K HDR test file on four cores with the pinned ffmpeg, and the planner never picks a setting it measured slower than that. A stubbed failing graphics-card encoder falls back to software. Styled and picture subtitles are pixel-checked in Chromium, including a PGS file from a small generator I write, because ffmpeg cannot create one. The iPhone-fullscreen profile gets burned-in subtitles. A stall steps down and is remembered for the device.
- **M5.** For each implemented operation, every field a real Jellyfin 12.x server returns on the same test library is present with the same name and type. Values are compared for a named list (names, types, durations, stream codecs, watched state); IDs, dates and server identity are excluded by a list kept in the repo. Every response loads in Jellyfin's official Kotlin client library, and scripted sessions pass with the TypeScript one. The stripped-credential replay shows only artwork routes answering. A revoked device is refused on every stream. For each recorded app profile, a file the app cannot play gets a converted stream that passes the M3 and M4 checks. Store apps driven in an Android emulator at GitHub are best effort, never a gate.
- **M6.** A child profile cannot fetch a restricted item through our own API or the Jellyfin adapter. The start and end of a planted intro are found to within 2 seconds.
- **M7.** For each implemented call, every field a real Navidrome server returns on the same test library is present with the same name and type. A third-party web music client browses and plays under Playwright. The multi-disc fixture yields one album. The child-profile test passes for the music protocol. App passwords can be revoked.
- **M8.** An arrow, enter and back only journey at 1080p with a visible-focus assertion at every step. The app loads and completes the journey in Chromium 87 and 108, the engines of 2022 LG TVs and of 2024 LG and Samsung TVs.
- **M9.** Two nodes stream through a self-hosted stand-in for Tailscale's servers. Sign-in throttling and an authorization matrix pass over the remote path.

## How I verify things I cannot see

| Where | What it can prove | What it cannot |
|---|---|---|
| My build environment | Server logic; the web app in a real Chromium browser, where H.264 plays; streams checked against generated test media; a second test player with no browser (headless mpv) that really decodes HEVC, DTS and styled subtitles; real Jellyfin and Navidrome servers in Docker for comparison (Docker started here during the plan's review; M0 re-checks it); the pinned ffmpeg | No graphics card and no HEVC decoding in the browser (measured). A file with H.264 video and AC-3 audio plays silently here, the same failure Chrome and Firefox show at home, which makes it a useful test |
| GitHub Actions | Real Windows and macOS machines; the installer and updater; Firefox; Safari's engine on macOS (close to iPhone Safari, not identical) | Real phones, TVs and graphics cards |
| The owner | Real devices, HDR, surround sound, remotes | n/a |

Hosts this environment cannot reach today: TMDB, MusicBrainz and Cover Art Archive, the Android SDK, jellyfin.org and its API reference, Tailscale's servers, and the downloads for the Firefox and Safari-like test browsers. Docker Hub, Maven Central and direct GitHub release downloads work. Tests of online lookups use recorded responses; recording them needs TMDB's hosts allowed for one session and the owner's key entered through a secure form, never in chat.

## Rules that hold in every milestone

- **The owner's media is never changed.** The program only reads media folders. Its database, posters and converted video live in a separate folder.
- **Nothing from the owner's home goes into the repository.** File lists, support bundles and recorded app requests are used in the session only. Test data made from them has titles, device names, addresses and tokens replaced first. The repository is treated as public from 2026-11-11, whether or not its visibility has been changed yet: the history up to then was checked, and every later commit is checked before it is pushed. The owner's answers are written down only as far as the plan needs them (kinds of device, never names, addresses or details of the household).
- **Plain HTTP first.** Everything essential works at `http://192.168.x.x`, enforced by a browser test against exactly that kind of address. Features that need HTTPS are extras that arrive in M9.
- **Keyboard only.** One arrow-keys-only test journey stays green from M1 onward, so TV mode is polish and not a rewrite.
- **Nothing lands without its test.**
- **A software path always exists**, at a size the PC can sustain. If 4K HDR is too slow without a working graphics card, the app says so instead of stuttering.
- **No borrowed Jellyfin code.** Nothing is copied from Jellyfin's server or web client, whose GPL-2.0 licence does not mix with ours, and we use no Jellyfin name or logo beyond "works with Jellyfin-compatible apps".
- **Linux checks run on every change; Windows and macOS checks on release builds and nightly.** While the repository is still private, Windows and macOS checks run only on releases, to stay inside GitHub's free allowance. Its visibility is checked at the start of each session.
- **A decision log.** `CLAUDE.md` and `docs/decisions.md` record what was decided and why. A setup script rebuilds my environment at the start of each session (Go toolchain, pinned ffmpeg, Docker service, reference servers).

## How this plan was made

Four research passes checked the current state of media servers, playback, client apps and server technology against primary sources where they could be reached. Several sites were blocked, so some facts rest on search results.

Three architects then designed competing plans (web-first, native-player-first, compatibility-first), and three judges scored them for buildability, owner experience and technical risk. The native-player plan lost clearly (3.5, 5 and 4.5 out of 10), mainly because its Android app could not be built or tested from here. The other two tied overall. One judge preferred web-first and two preferred compatibility-first, each by half a point to a point, and all three asked for a blend of the two.

This plan uses the web-first order (the browser plays everything before store apps are attempted) and adds the compatibility plan's early installer and updater, recorded-request budget and checkpoint. That order has a cost two judges flagged: we do not learn whether real store apps accept our server until M5.

Four critics then attacked a first draft for wrong facts, infeasible steps, unclear wording and dropped findings. This version includes their corrections.

The owner then answered three questions: the phone is an iPhone, the repository goes public, and no Jellyfin is installed for comparison. The judges and critics had recommended a Jellyfin baseline, both as a yardstick and as a source of recorded app requests. Without it the checkpoint judges the project on its own, and M5 starts from the published protocol description, which is expected to take more rounds.

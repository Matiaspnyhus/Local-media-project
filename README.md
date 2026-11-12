# Local Media (working title)

A small media server for one household: one program on a PC that serves your movies, shows and music to the other screens at home, through a web app it hosts itself.

**Status: plays most things (milestone M3).** It keeps a library of films and shows with posters and descriptions, installs and updates itself on Windows, and plays on the PC and on other devices at home. A file a device cannot play as it is gets repackaged while it plays, with its sound converted if need be; the picture is never changed. Files whose picture a device cannot show are the next milestone. The plan is in [PLAN.md](PLAN.md).

If you are the owner trying a build, follow the newest checklist in [docs/checklists](docs/checklists).

## Building it yourself

Needs Go and Node.js. Repackaging and the tests for it also need `ffmpeg` and `ffprobe` on the PATH (or named in `LOCALMEDIA_FFMPEG` and `LOCALMEDIA_FFPROBE`); the version this project ships and tests with is pinned in `packaging/ffmpeg.env`.

```
make build      # builds the web app, then the program (./localmedia)
make verify     # tests, then a keyboard-only browser test against the real program
make cross      # builds for Windows, macOS and Linux into release/
```

Run `./localmedia` and open `http://localhost:8177`. Its database and logs go in a `localmedia` folder in your user profile; media folders are only ever read.

Licence: GPL-3.0-or-later. See [LICENSE](LICENSE).

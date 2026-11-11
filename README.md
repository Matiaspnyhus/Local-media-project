# Local Media (working title)

A small media server for one household: one program on a PC that serves your movies, shows and music to the other screens at home, through a web app it hosts itself.

**Status: first playable build (milestone M0).** It finds video files in a folder and plays the ones a browser can play by itself, on the PC and on other devices at home. The plan for everything after that is in [PLAN.md](PLAN.md).

If you are the owner trying a build, follow [docs/checklists/M0.md](docs/checklists/M0.md).

## Building it yourself

Needs Go and Node.js.

```
make build      # builds the web app, then the program (./localmedia)
make verify     # tests, then a keyboard-only browser test against the real program
make cross      # builds for Windows, macOS and Linux into release/
```

Run `./localmedia` and open `http://localhost:8177`. Its database and logs go in a `localmedia` folder in your user profile; media folders are only ever read.

Licence: GPL-3.0-or-later. See [LICENSE](LICENSE).

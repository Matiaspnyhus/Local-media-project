# File-name test set

`corpus.json` holds real-world file and release names with the answer each should give. The answers were written by the authors of two other parsers, not by us, so our parser is not marking its own homework.

Sources:
- guessit's test files `movies.yml` and `episodes.yml` (https://github.com/guessit-io/guessit, LGPL-3.0). Entries that need special options, or that have no plain title, season and episode, were left out.
- PTT's `tests/test_main.py` and `tests/test_title.py` (https://github.com/dreulavelle/PTT, MIT).

Each entry has `kind` (`movie`, `episode`, or `any` when the source only states the title), `title`, and where known `year`, `season` and `episode`.

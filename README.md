# AI Mood Playlists

A [Navidrome](https://www.navidrome.org/) plugin that auto-generates and maintains playlists — one per genre or
mood — built entirely from tags written by the
[AI Auto-Tagging](https://github.com/RFLundgren/AI-auto-tagging-plugin) plugin. It's a fork of
[navidrome-mood-plugin](https://github.com/RFLundgren/navidrome-mood-plugin), reworked to read AI-classified tags
instead of running its own audio analysis.

## Status

**Working, tested end-to-end in production.** Builds cleanly via TinyGo, 24 unit tests pass natively (no WASM
runtime needed), and it's been run against a real library with real AI-Auto-Tagging-written tags.

## Why this fork exists

The original plugin's audio-analysis approach has a structural limitation, not just a tuning problem: the
essentia/TensorFlow models it uses score audio *texture* — tempo, spectral brightness, dynamics — not cultural
context. A slow, quiet metal track can legitimately score high on "relaxed" and land in Chill Mix, because nothing
in the model knows "this is metal, therefore not chill." This fork replaces that classification layer with tags
from AI Auto-Tagging, which uses an LLM reasoning over artist/genre/title context instead — a fundamentally
different (and for this specific failure mode, better-suited) kind of signal.

The original plugin and its audio-analysis approach are untouched — this is a separate copy, not a modification,
for anyone who wants to keep using it as-is.

## This plugin only reads tags — it doesn't write any

It has a hard dependency on [AI Auto-Tagging](https://github.com/RFLundgren/AI-auto-tagging-plugin): install and
configure that first, and let it cover a meaningful chunk of your library before expecting useful playlists here.
If `getAllUserTags.view` comes back empty or thin, that's AI Auto-Tagging not having classified enough yet, not a
bug in this plugin.

No AI provider cost of its own — this plugin only calls Navidrome's own Subsonic API (free, local). All AI token
cost belongs to AI Auto-Tagging; see that repo's README for its cost disclaimer.

## How it works

1. On a configurable schedule (hourly by default — cheap, since it's just reading tags someone else already
   computed, not doing any analysis itself), discovers every distinct tag value AI Auto-Tagging has written via
   `getAllUserTags.view`, filtered to the configured categories (`genre`/`mood` by default).
2. For each qualifying tag value, gathers every track carrying it (`getSongsByUserTag.view`), shuffles, walks the
   shuffled list applying a per-artist cap, stops once the configured playlist size is reached, and
   creates/updates a playlist via `createPlaylist`/`updatePlaylist`.
3. Playlist names are prefixed `AI ` by default (e.g. `AI Chill Mix`, `AI Rock`) specifically so they never
   collide with the original audio-analysis plugin's playlist names if both are installed side by side — its
   "createPlaylist" name-prefix matching would otherwise find and silently overwrite the wrong playlist. This
   prefix is configurable — see **Naming your playlists** below.
4. Instant Mix is reimplemented around shared-tag overlap: similar tracks are ranked by how many tags they share
   with the source track, instead of continuous audio-vector distance (which discrete tags can't represent).

Playlist cover art is not something this plugin sets — Navidrome automatically generates a 4-tile mosaic from a
playlist's own tracks' album art the moment it has tracks, so this comes for free with zero extra work.

## Picking which playlists get created

There are two config fields for this — **Genres to Build Playlists For** and **Moods to Build Playlists For** —
each a plain comma-separated text box. When you first open the config screen, both are already filled in with
AI Auto-Tagging's full built-in vocabulary (25 genres, 12 moods), so out of the box every genre/mood value it has
used gets its own playlist — up to 37 total.

To build playlists for only some genres or moods, **delete the ones you don't want** from the text, keeping the
comma-separated format. For example, if the Genres field starts as:

```
rock, pop, electronic, hip hop, jazz, classical, metal, folk, country, r&b, soul, blues, reggae, punk, indie, ambient, new age, world, funk, disco, house, techno, alternative, soundtrack, experimental
```

and you only want playlists for rock, jazz, and metal, edit it down to:

```
rock, jazz, metal
```

Anything you remove from the list simply won't get a playlist — the underlying tag still exists and tracks are
still tagged with it, this field only controls which tag values get turned into playlists. You can also add words
that aren't in the default list; a playlist gets built for a name in this field only if at least one track is
actually tagged with that value, so adding a genre AI Auto-Tagging never uses has no effect.

Clearing a field entirely (deleting all the text so it's blank) means **no playlists at all** for that category —
this is different from leaving it untouched, which builds all of them.

> **Note on existing tags**: these default lists mirror AI Auto-Tagging's *current* fixed vocabulary. If your
> library was tagged before that vocabulary was constrained, it may have older, messier tag values that aren't in
> this list — those won't get playlists until you either add them to this field by hand or re-tag your library
> to match the current vocabulary. See
> [AI Auto-Tagging's PLAN.md](https://github.com/RFLundgren/AI-auto-tagging-plugin/blob/master/PLAN.md) for the
> cleanup approach used to clear old tags and let them get re-classified.

## Naming your playlists

Every auto-generated playlist name starts with a prefix — `AI ` by default, e.g. `AI Chill Mix`, `AI Rock`. This
exists so the plugin's playlists are easy to spot in your library and so they never collide with playlists you
already have (see the collision note above).

The **Playlist Name Prefix** config field lets you change this to whatever you like. For example, setting it to
`Auto: ` produces `Auto: Chill Mix` and `Auto: Rock` instead. Leaving it blank restores the default `AI `.

If you change the prefix after playlists already exist under the old one, the old-prefixed playlists are left
as-is (they're orphaned, not renamed or deleted) and new ones are created under the new prefix on the next
rebuild — so it's worth deleting the old ones by hand if you don't want duplicates.

## Configuration

Set via Navidrome's Admin → Plugins → AI Mood Playlists → Config, after installing the `.ndp` package:

| Field | Default | Notes |
|---|---|---|
| `navidrome_url` | `http://navidrome:4533` | Internal URL of your Navidrome server |
| `navidrome_user` / `navidrome_password` | — | Needed because scheduled rebuilds run outside a user request context — same reason AI Auto-Tagging needs a `libraryUser` |
| `rebuild_schedule` | `0 * * * *` (hourly) | Cron expression for how often to rebuild playlists |
| `playlist_tag_categories` | `genre,mood` | Which tag categories to build playlists from at all |
| `genre_allowlist` / `mood_allowlist` | full built-in vocabulary (pre-filled, editable) | Comma-separated list of genre/mood values to build a playlist for. Delete entries to narrow it down, clear entirely to build none for that category. See **Picking which playlists get created** above |
| `playlist_name_prefix` | `AI ` | Text prepended to every auto-generated playlist name. See **Naming your playlists** above |
| `playlist_size` | `50` | Tracks per playlist |
| `max_tracks_per_artist` | `3` | Per-artist cap per playlist (0 = no limit) |
| `similar_songs_count` | `20` | Tracks returned for Instant Mix |
| `max_concurrency` | `2` | Concurrent rebuild tasks |
| `show_dates_in_title` | `true` | Append the last-rebuild timestamp to playlist names |
| `enrich_playlists` / `enrich_schedule` | `false` / `0 5 * * *` | Optional: add creation-date metadata to your *other*, non-generated playlists too |

The plugin also needs the **Users Permission** grant (Admin → Plugins → AI Mood Playlists) for whichever user
matches `navidrome_user`.

## Installing

1. Build `ai-mood-playlists.ndp` (see Building below), or get one from wherever you're distributing it
2. Copy it to `<navidrome-data>/plugins/`
3. Restart Navidrome (or use `ND_PLUGINS_AUTORELOAD=true`)
4. Admin → Plugins → AI Mood Playlists → grant **Users Permission**, then fill in **Config**

## Building

```bash
tinygo build -o plugin.wasm -target wasip1 -buildmode=c-shared .
```

Package into a `.ndp` (the wasm file must be named exactly `plugin.wasm` inside the zip):

```powershell
# Windows
Compress-Archive -Path manifest.json,plugin.wasm -DestinationPath ai-mood-playlists.zip
Rename-Item ai-mood-playlists.zip ai-mood-playlists.ndp
```

```bash
# Linux/macOS
zip -j ai-mood-playlists.ndp manifest.json plugin.wasm
```

Run tests (no TinyGo/WASM runtime needed — the PDK provides mocks for native builds):

```bash
go test ./...
```

## Relationship to navidrome-mood-plugin

This is a **separate copy** with its own git history, not a modification of
[navidrome-mood-plugin](https://github.com/RFLundgren/navidrome-mood-plugin) — that repo keeps working exactly as
it is for anyone using its audio-analysis approach. See [PLAN.md](PLAN.md) for the full rework design: what was
kept from the original (playlist upsert mechanics, per-artist diversity cap), what was retired (the Python
analyzer service, all continuous mood scores), and the reasoning behind each decision.

`HELP.md` still describes the original's audio-analysis architecture in full — it hasn't been rewritten for this
fork yet (tracked in `PLAN.md`'s remaining work).

## License

GPL-3.0 — same as the original.

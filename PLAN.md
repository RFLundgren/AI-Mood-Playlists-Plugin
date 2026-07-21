# AI Mood Playlists — Design & Build Plan

## Context

Forked from [navidrome-mood-plugin](https://github.com/RFLundgren/navidrome-mood-plugin) (an audio-analysis-based
mood-playlist plugin) because its classification is documented as unreliable in ways that are structural, not
tunable: the essentia/TensorFlow models score audio texture (tempo, spectral brightness, dynamics), not cultural
mood context. Its own `future-roadmap.md` names the failure mode directly — a slow, quiet Rammstein track scores
high on "relaxed" and lands in Chill Mix, because nothing in the model knows "this is metal, therefore not chill."
Existing mitigations (genre-keyword score boosts, Last.fm crowd-tag adjustments) are explicitly self-described as
patching symptoms, not fixing the signal.

Direction: keep this fork's playlist-construction logic (which is still genuinely useful — per-artist diversity
cap, a "variation pool" shuffle for weekly freshness), but replace the classification layer entirely with tags
already written by [AI Auto-Tagging](https://github.com/RFLundgren/AI-auto-tagging-plugin), which classifies via an
LLM reasoning over artist/genre/title context rather than raw audio signal.

This is a **separate copy**, not a modification of the production `navidrome-mood-plugin` fork — other people rely
on that one running as-is. Git history was preserved on copy; the `origin` remote was deliberately removed so
nothing here can accidentally get pushed to the production repo.

## Status at a glance

| Piece | Status |
|---|---|
| Repo forked, renamed, rebuilds cleanly | ✅ Done |
| Docs: forward-looking note + cost/idempotency section in `HELP.md` | ✅ Done |
| Decision: Instant Mix's fate | ✅ Decided — reimagine around shared-tag overlap |
| Decision: playlist mechanism | ✅ Decided — simplified, tag-based snapshot rebuild, running frequently |
| Actual code rework | 📋 Not started — `main.go` is currently byte-for-byte the original audio-analysis implementation |

## Architecture reference: what the original does today

For whoever reads this cold later — the starting point, before any rework:

- **Two-part system**: a Go WASM plugin (`main.go`) plus a separate Python FastAPI audio-analysis microservice
  (Essentia + TensorFlow models, Docker container), talking over plain HTTP/JSON.
- **Scheduling**: five independent cron jobs — nightly library scan for unanalyzed tracks, weekly full playlist
  rebuild, monthly re-analysis of low-confidence tracks, nightly genre-tag migration, nightly cosmetic
  "creation date" enrichment.
- **Storage**: mood scores live in the plugin's own KV store (`mood:<trackID>`, plus a `mood:index` of
  id→title/artist) — **not** Navidrome user tags. No `setUserTag`/`getUserTags` calls exist anywhere in it today.
- **Playlists are NOT built on Navidrome's native smart-playlist criteria.** Membership is a static snapshot:
  each refresh walks the whole KV store, scores/filters/sorts candidates per mood definition, applies a per-artist
  cap and a "variation pool" shuffle (draws a random subset from the top N×pool candidates, for week-to-week
  variety), then upserts the playlist via `createPlaylist`/`updatePlaylist` with an explicit list of track IDs.
  Playlist identity across runs is found by name-prefix matching, not a stored ID. This is *why* a weekly refresh
  job exists at all — nothing re-evaluates membership between runs.
- **Instant Mix** is a separate feature from the 13 mood playlists — nearest-neighbor search over each track's
  continuous 5-dimension mood vector (happy/sad/relaxed/aggressive/party), returned directly, no playlist involved.

## Decisions

### Decision 1 — What happens to Instant Mix? ✅ Decided: reimagine around shared-tag overlap

Its original design depends on continuous numeric vectors for distance computation; discrete AI tags
(`genre:rock`, `mood:chill`) don't map cleanly onto "how similar," only "same or not." Rather than keep the Python
analyzer service alive just for this one feature, or drop it, nearest-neighbor becomes "tracks sharing the most
genre/mood tags with the source track." Coarser than continuous similarity, but removes the audio-analysis
dependency entirely, consistent with the rest of this rework.

### Decision 2 — Playlist mechanism ✅ Decided: simplified, tag-based snapshot rebuild, run frequently

Native smart-playlist criteria (`{"is": {"usertag": "mood:chill"}}`, `sort: "random"`, `limit: N`) looked
attractive initially — self-maintaining, far less code, and Navidrome's `random` sort genuinely re-randomizes on
every view (confirmed against `persistence/criteria_sql_test.go` in `navidrome-experimental` — it compiles to SQL
`random() asc`, not a cached shuffle).

**The blocker: per-artist diversity.** The library this is built for has extreme artist skew (some artists with
60+ albums, others with just one), so an unweighted random draw would let big artists dominate every mood
playlist. That's not solvable with native criteria — Navidrome's rules engine has no per-group-limit concept, and
more fundamentally, "cap tracks per artist" requires evaluating a whole candidate set at once to select a balanced
subset, which is incompatible with a rule that re-randomizes independently on every single view. There's nothing
stable to cap.

So: **keep a periodic rebuild, adapted for tags, but much simpler and much more frequent than the original.** The
original plugin's weekly cadence existed because *audio analysis* was expensive — that cost is now paid once, ever,
per track, by AI Auto-Tagging (see its own idempotent-classification design). Reading already-written tags via
`getUserTags`/`search3` is cheap, so the rebuild itself can run far more often (hourly, or whatever cadence feels
right) without meaningful cost, closing most of the "staleness" gap a scheduled job implies. The rebuild logic
itself:

1. Gather all tracks matching a tag value (e.g. everything tagged `mood:chill`)
2. Shuffle
3. Walk the shuffled list, applying the configured per-artist cap as you go
4. Stop once the configured playlist size is reached
5. Upsert via `createPlaylist`/`updatePlaylist`, same mechanism as today

This is genuinely less code than the original (no score thresholds, no confidence-based re-analysis, no
genre-boost weighting to port) while still guaranteeing the artist diversity this library actually needs.

## Remaining implementation work

1. Retire the Python analyzer-service dependency and every HTTP call to it in `main.go`.
2. Replace KV-stored mood-score reads/writes with `getUserTags`/`search3` calls against AI Auto-Tagging's tags.
3. Implement the simplified rebuild logic from Decision 2 above (gather → shuffle → per-artist-cap walk → size
   limit → upsert), with playlist size and per-artist cap as config values.
4. Decide the actual playlist set — keep the same 13 named mixes (mapped from tag values instead of score
   thresholds), or move to something more dynamic (e.g. one playlist auto-created per distinct `genre:`/`mood:`
   tag value discovered in the library). Still open — smaller in scope than the two decisions above.
5. Implement Instant Mix's tag-overlap reimplementation per Decision 1 above.
6. Trim `manifest.json`'s config schema — remove now-obsolete fields (mood thresholds, analyzer URL, Last.fm
   key/weights, genre-boost-weight; genre exclusions become unnecessary if per-mix criteria can just exclude a
   genre tag directly) and add whatever the tag-based approach actually needs instead.
7. Add native-build unit tests. This repo currently has none — `main.go` is gated `//go:build wasip1` with no
   stub/mock files for native `go test`, unlike AI Auto-Tagging-Plugin's PDK-mock-based suite. Worth adding the
   same pattern before relying on a live test alone.
8. Full rewrite of `HELP.md` once the above lands — right now it only has a forward-looking note and one new
   section; the bulk of the document still describes the old audio-analysis architecture start to finish.
9. Live end-to-end test on production, same careful/staged approach as AI Auto-Tagging-Plugin got (small scope
   first, watch logs, verify results before scaling up). Likely lower-risk here since this plugin doesn't call
   any AI provider itself — it only reads tags AI Auto-Tagging already wrote, so there's no per-run API cost to
   worry about.
10. Decide whether/when to retire the original audio-analysis Docker service on the production instance, or run
    it in parallel for as long as other people are still using the original plugin.
11. Decide on a GitHub remote / whether to publish this fork at all — no remote configured currently, by design.

## Dependency on AI Auto-Tagging-Plugin

This project can't do anything useful until AI Auto-Tagging has actually classified a meaningful chunk of the
library — it reads tags, it never writes them. Practically: let AI Auto-Tagging reach reasonable library coverage
before testing playlist generation here, or every playlist will come back empty for reasons that have nothing to
do with this plugin's own logic.

**No direct AI provider cost of its own** — this plugin only calls Navidrome's own Subsonic API (free, local), never
an AI provider directly. All AI token cost belongs to AI Auto-Tagging; see that repo's README for its cost
disclaimer.

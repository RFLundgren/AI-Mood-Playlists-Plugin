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
| Decision: Instant Mix's fate | ❓ Undecided |
| Decision: playlist mechanism (native smart-playlist criteria vs. keep manual snapshot rebuild) | ❓ Undecided |
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

## Two decisions needed before real implementation starts

### Decision 1 — What happens to Instant Mix?

Its current design depends on continuous numeric vectors for distance computation; discrete AI tags
(`genre:rock`, `mood:chill`) don't map cleanly onto "how similar," only "same or not."

- **A. Keep it running on the old audio vectors in parallel** — preserves behavior exactly, but keeps the Python
  analyzer service (the thing this whole rework exists to get away from) alive for one feature.
- **B. Reimagine it around shared-tag overlap** — nearest neighbor = most matching tags. Loses fine-grained
  continuous similarity, but removes the audio-analysis dependency entirely.
- **C. Drop it from this fork.** Simplest, but a real feature loss for anyone using it today.

No recommendation locked in — this is a product call.

### Decision 2 — Playlist mechanism: native smart-playlist criteria, or keep the manual snapshot-rebuild model?

- **Native criteria** (e.g. `{"is": {"usertag": "mood:chill"}}`, self-maintaining, no scheduled rebuild needed) —
  much less code, but Navidrome's rules engine has no direct equivalent for the existing per-artist cap or
  variation-pool shuffle. Those specific behaviors would need to be dropped or reimplemented some other way.
- **Keep the manual snapshot-rebuild model**, adapted to read tags instead of audio vectors — preserves
  per-artist-cap and variation-pool exactly as they work today, but keeps the weekly-refresh scheduling
  machinery even though nothing about tag-based classification actually *requires* a refresh cadence (the tags
  themselves only change when AI Auto-Tagging classifies a new track, not on any regular schedule).

Leaning toward native criteria for simplicity's sake, but not settled — worth deciding deliberately rather than
defaulting into it, especially since the whole reason a "weekly refresh" job exists today is the snapshot design,
not a technical requirement of the new data source.

## Remaining implementation work (once the two decisions above are made)

1. Retire the Python analyzer-service dependency and every HTTP call to it in `main.go`.
2. Replace KV-stored mood-score reads/writes with `getUserTags`/`search3` calls against AI Auto-Tagging's tags.
3. Rebuild playlist-construction logic per Decision 2.
4. Decide the actual playlist set — keep the same 13 named mixes (mapped from tag values instead of score
   thresholds), or move to something more dynamic (e.g. one playlist auto-created per distinct `genre:`/`mood:`
   tag value discovered in the library).
5. Resolve Instant Mix per Decision 1.
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

# Future Roadmap: Advanced Plugin Features

This document outlines the architectural plans and implementation steps for four major future enhancements to the `navidrome-mood-plugin`. These features transition the plugin from a library analysis utility into a highly personalized, reactive music discovery assistant.

---

## Feature 1: "Mood of the Day" Rotating Playlist

**Concept:** A single, dynamic playlist that changes its entire theme every 24 hours to solve the "paradox of choice" and encourage library exploration.

### Architectural Plan
This is the lowest-friction feature to implement as it leverages the existing playlist generation logic.

1. **Schedule Logic:**
   - Add a new cron schedule (e.g., `0 6 * * *` - 6 AM daily) specifically for the "Mood of the Day" task.
2. **Selection Algorithm:**
   - The plugin evaluates `time.Now().Weekday()`.
   - Map specific days to specific vibes to create a reliable rhythm (e.g., Monday = Workout/Energetic, Friday = Party, Sunday = Chill/Study).
   - *Alternative:* Implement a weighted randomizer so the user never knows what vibe they will get until they open the app.
3. **Playlist Generation:**
   - The plugin selects the 50 best tracks for the chosen mood.
   - It updates a dedicated Subsonic playlist explicitly named `Daily Vibe` or `Mood of the Day`.
4. **Metadata Updates:**
   - Update the description/comment of the playlist to explain the choice: *"Today's Vibe: High Energy Workout Mix. Generated at 6:00 AM."*
   - Update the playlist title (if the user has titles enabled) to reflect the day: `Daily Vibe (Monday: Energetic)`.

---

## Feature 2: Per-User Mood Playlists

**Concept:** Generating personalized mood mixes based on the specific listening habits and library access rights of individual users on a shared Navidrome server.

### Architectural Plan
Currently, playlists are generated globally under the admin account. This requires shifting to a user-centric loop.

1. **User Discovery:**
   - The plugin must query the Navidrome database/API to retrieve a list of all active users.
2. **Personalization Filtering (The Magic):**
   - For each user, the plugin fetches their "Starred/Favorited" tracks and "Top Played" tracks using the Subsonic API (e.g., `getStarred`, `getTopSongs`).
   - The plugin cross-references these user-specific IDs against the global `mood:index`.
   - Tracks that the user actively listens to receive an artificial "Score Boost" (e.g., +0.2 to their Happy score for that specific user's playlist).
3. **API Execution:**
   - The plugin iterates through the users.
   - It executes the `createPlaylist` / `updatePlaylist` Subsonic API calls *using that specific user's credentials or context*, ensuring the playlist is owned by them and only visible to them.

---

## Feature 3: Configurable Composite Thresholds

**Concept:** Allowing power users to easily tweak the complex mathematical rules behind composite playlists (e.g., changing "Study Mix" to allow slightly more Energy) without requiring coding knowledge.

### Architectural Plan
Requiring users to type JSON is bad UX. Instead, we lean into Navidrome's native `manifest.json` UI capabilities by creating collapsible configuration groups for the most popular composite playlists.

1. **UI Implementation (`manifest.json`):**
   - Add new `Group` elements to the `uiSchema` to visually box and organize these advanced settings, keeping the main settings page clean.
   - Example grouping: **"Advanced: Study Mix Tuning"**
     - Field: `study_max_energy` (Type: Number, Title: "Max Energy", Default: 0.15)
     - Field: `study_max_aggressive` (Type: Number, Title: "Max Aggression", Default: 0.20)
     - Field: `study_min_relaxed` (Type: Number, Title: "Min Relaxed Score", Default: 0.45)
2. **Go Parsing Logic (`main.go`):**
   - Currently, composite rules are hardcoded in the `compositeMoods` struct.
   - We will update the `refreshPlaylists` function to dynamically pull these values from `configFloat("study_max_energy", 0.15)`.
3. **UX Considerations:**
   - To avoid overwhelming the user, we don't need to expose *every* parameter for *every* playlist immediately. We can start by exposing the tuning dials for the top 3 most subjective playlists (e.g., Study, Workout, Sleep) where users are most likely to disagree on the default thresholds.

---

## Feature 4: Last.fm Reactive Listening Integration

**Concept:** The "Holy Grail" feature. The plugin reads a user's recent Last.fm scrobbles, calculates the average mood of what they are currently listening to, and instantly generates a "Current Vibe" playlist that matches their real-world mood.

### Architectural Plan
This is the most technically complex feature, requiring external API polling and fuzzy matching.

1. **Authentication:**
   - Add fields in the plugin settings for `Last.fm Username` and `Last.fm API Key`.
2. **Polling Engine:**
   - Implement a rapid background task (e.g., every 30-60 minutes).
   - The plugin queries the Last.fm `user.getRecentTracks` API to fetch the last 10 tracks played by the user.
3. **Fuzzy Matching (The Hard Part):**
   - Last.fm returns plain text (e.g., `"The Beatles" - "Let It Be"`). 
   - The plugin must query the local Navidrome API (or its internal `mood:index`) to find the corresponding Subsonic `Track ID`.
   - String normalization (lowercasing, stripping "Remastered 2009" tags) is required to ensure high match rates.
4. **Mood Calculation & Generation:**
   - Once the Subsonic IDs are found, the plugin pulls their mood scores from the KVStore.
   - It calculates the average vector (e.g., "The user's last 10 tracks average an Energy of 0.85 and a Sadness of 0.90").
   - The plugin queries the rest of the library to find the 30 closest matching tracks using Euclidean distance (similar to Instant Mix).
   - It updates a dedicated `Current Vibe` playlist.

---

## Mood Classification Accuracy Improvements

### Background

The current essentia-tensorflow mood models detect **audio texture** — tempo, spectral brightness, dynamics — not cultural mood context. This produces results like Rammstein appearing in Chill Mix: the model correctly identifies a track with low tempo and low spectral brightness, but has no concept of "this is metal therefore it is not chill." The genre boost system in `app.py` partially compensates, but only fires when genre tags match specific keywords and only adjusts scores rather than enforcing hard rules.

The options below form a progression from a quick tactical fix to a full architectural replacement. They are not mutually exclusive — each builds on the previous.

---

### Option 1: Configurable Per-Mix Genre Exclusions *(Immediate — in progress)*

**What it does:** Adds comma-separated genre exclusion lists to the plugin config UI, one per affected mix (Chill, Sleep, Study, Dining, Background, Road Trip). Tracks whose genre tag matches any excluded keyword are ineligible for that mix regardless of their mood scores. Empty field = revert to hardcoded defaults.

**Changes required:**
- `analyzer-service/app.py` — return `genre` field in JSON response
- `main.go` — store genre in KV, read config fields, apply exclusion at playlist generation
- `manifest.json` — add six new string config fields in a Genre Exclusions UI group

**Defaults:**

| Mix | Default Exclusions |
|-----|-------------------|
| Chill | metal, hard rock, punk, hardcore, industrial, grunge, thrash |
| Sleep | metal, hard rock, punk, hardcore, industrial, grunge, thrash, dance, techno, trance, house, electronic, edm, drum and bass |
| Study | metal, punk, hardcore, industrial |
| Dining | metal, hard rock, punk, hardcore, industrial |
| Background | metal, hard rock, punk, hardcore, industrial |
| Road Trip | metal, hardcore, industrial |

| Dimension | Rating | Notes |
|-----------|--------|-------|
| Effort | Low | ~3 files, straightforward changes |
| Risk | Low | Additive only, existing behaviour unchanged if fields left empty |
| Value | High | Immediately fixes obvious misclassification for well-tagged libraries |
| Dependency | Good genre tags in library | Falls back gracefully if tags missing |

**Limitations:** Entirely dependent on genre tag quality. Obscure or poorly tagged tracks bypass the filter. Does not improve scores — just blocks bad results.

---

### Option 2: Last.fm Crowd-Sourced Tag Integration *(Medium Term)*

**What it does:** During analysis, queries the Last.fm API for user-generated tags on each track (e.g. "chill", "aggressive", "relaxing", "metal"). These tags represent cultural consensus rather than audio texture. Last.fm tags are blended with essentia scores — if Last.fm says "aggressive metal", that overrides a high relaxed score.

**Changes required:**
- `analyzer-service/app.py` — add Last.fm API call per track using artist+title, parse top tags, apply tag-to-score mapping
- `manifest.json` / plugin config — add optional `lastfm_api_key` field
- No changes to `main.go` — scores arrive pre-adjusted

**Blending strategy:**
- If Last.fm returns tags matching a mood keyword (e.g. "chill", "relaxing") → boost `mood_relaxed`
- If Last.fm returns tags matching aggressive keywords (e.g. "metal", "heavy") → boost `mood_aggressive`, reduce `mood_relaxed`
- Weight Last.fm influence at ~30–40% to avoid over-riding valid audio analysis

| Dimension | Rating | Notes |
|-----------|--------|-------|
| Effort | Medium | API integration, tag mapping, rate limiting, fallback handling |
| Risk | Low-Medium | External dependency; falls back to essentia-only if API unavailable |
| Value | High | Accurate for the vast majority of popular/known music |
| Dependency | Last.fm API key (free), internet access from analyzer container, track must exist in Last.fm database |

**Limitations:** Unknown, local, or obscure tracks have no Last.fm data. API rate limit is 5 req/sec — analysis of 10k tracks adds ~30 min to a full pass. Not useful for home recordings or rare bootlegs.

**Note:** Partially overlaps with Feature 4 (Last.fm Reactive Listening). The API key config could be shared.

---

### Option 3: CLAP Model — Language-Audio Understanding *(Long Term)*

**What it does:** Replaces or supplements essentia with CLAP (Contrastive Language-Audio Pretraining), a model that jointly understands audio and natural language. You query it with text prompts like "relaxing background music" or "aggressive metal" and it returns a similarity score for any track. It understands cultural context because it was trained on text descriptions of audio.

**Changes required:**
- `analyzer-service/app.py` — load CLAP model, define mood prompt set, run inference per track, map similarity scores to mood fields
- Dockerfile — add CLAP model weights (~1GB), torch dependency
- No changes to plugin (`main.go`, `manifest.json`) — same score fields, same API contract

**Example mood prompts:**
```
mood_relaxed  → "calm, relaxing, peaceful background music"
mood_aggressive → "aggressive, intense, heavy metal music"
mood_happy    → "happy, upbeat, feel-good music"
mood_sad      → "sad, melancholy, emotional music"
```

| Dimension | Rating | Notes |
|-----------|--------|-------|
| Effort | High | Model integration, prompt engineering, inference pipeline rewrite |
| Risk | Medium | Model quality depends on prompt tuning; may need iteration |
| Value | Very High | Genuine cultural mood understanding, not just audio texture |
| Dependency | GPU strongly recommended for 10k tracks; CPU viable but slow (~5–10x slower than essentia) |

**Limitations:** Significantly heavier Docker image. Without a GPU, full library analysis would take much longer. Prompt wording affects results and may require tuning per library style. CLAP is less accurate for BPM/danceability — essentia would still be needed for those.

**Recommended approach if pursuing this:** Run CLAP alongside essentia, use CLAP for mood classification and essentia for BPM/danceability/energy. This is a hybrid rather than a full replacement.

---

### Option 4: Full Hybrid — Essentia + Last.fm + CLAP *(Long Term)*

**What it does:** Combines all three signal sources with a weighted scoring system:
- **Essentia** (40%) — BPM, danceability, energy (audio texture, reliable)
- **Last.fm** (35%) — cultural consensus tags for known tracks
- **CLAP** (25%) — semantic audio understanding as a tiebreaker / fallback for unknown tracks

Tracks with good Last.fm coverage get accurate cultural classification. Tracks without Last.fm data (local recordings, bootlegs, obscure releases) fall back to CLAP + essentia.

| Dimension | Rating | Notes |
|-----------|--------|-------|
| Effort | Very High | All three integrations plus blending logic |
| Risk | Medium | More moving parts, harder to debug when a track scores unexpectedly |
| Value | Very High | Most accurate possible result across all library types |
| Dependency | All of the above |

**Recommended path:** Only worth pursuing after Option 2 and Option 3 are individually proven to work well. The weights (40/35/25) should be configurable.

---

### Recommended Progression

```
Now          Option 1 (Genre Exclusions)    Quick win, well-tagged library
             ↓
Near term    Option 2 (Last.fm Tags)        Cultural accuracy for known music
             ↓
Long term    Option 3 (CLAP)                Semantic understanding for all tracks
             ↓
Aspirational Option 4 (Full Hybrid)         Best possible accuracy
```

Each step is independently valuable and the plugin remains fully functional at every stage.
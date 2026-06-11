# navidrome-mood-plugin

A [Navidrome](https://www.navidrome.org/) plugin that creates mood-based playlists using real audio analysis. It uses [essentia-tensorflow](https://essentia.upf.edu/) with Discogs-EffNet embeddings to analyze your music library for mood, energy, BPM, and danceability, then automatically generates and refreshes 13 mood playlists.

Works with any Subsonic-compatible client (Symfonium, Sublime Music, etc.).

## Features

- **13 Mood Playlists** — 6 simple moods + 7 composite scenario playlists, all auto-created and refreshed
- **Mood-Aware Instant Mix** — Replaces the default Instant Mix with mood-similarity matching (Euclidean distance across mood vectors)
- **Scheduled Analysis** — Periodically scans your library for new tracks and sends them to the analyzer
- **Scheduled Refresh** — Regenerates mood playlists on a cron schedule so they evolve as your library grows
- **Genre Exclusions** — Per-playlist configurable genre blocklists prevent misclassified tracks from appearing in calm/chill mixes
- **Playlist Dates & Timestamps** — Automatically tracks when mood playlists are generated and syncs creation dates to all your playlists (visible in Subsonic clients and optionally the Navidrome UI)
- **Fully Configurable** — Mood thresholds, genre exclusions, playlist sizes, analysis/refresh schedules all configurable from the plugin settings UI

### Mood Playlists

**Simple moods** — single score above a configurable threshold:

| Playlist | Based on | Default Threshold |
|----------|----------|-------------------|
| Happy Mix | mood_happy | 0.55 |
| Chill Mix | mood_relaxed | 0.40 |
| Energetic Mix | danceability | 0.60 |
| Melancholy Mix | mood_sad | 0.45 |
| Party Mix | mood_party | 0.55 |
| Aggressive Mix | mood_aggressive | 0.55 |

**Composite moods** — require a positive attribute AND cap negative ones:

| Playlist | Requires | Excludes | Sorted by |
|----------|---------|---------|-----------|
| Study Mix | relaxed ≥ 0.40 | aggressive ≥ 0.45, party ≥ 0.50 | relaxed |
| Workout Mix | danceability ≥ 0.50 | relaxed ≥ 0.60, sad ≥ 0.50 | danceability |
| Sleep Mix | relaxed ≥ 0.30 | aggressive ≥ 0.30, party ≥ 0.35 | relaxed |
| Road Trip Mix | happy ≥ 0.35 | aggressive ≥ 0.40, sad ≥ 0.50 | happy |
| Cooking Mix | happy ≥ 0.35 | aggressive ≥ 0.45, sad ≥ 0.45 | happy |
| Dining Mix | relaxed ≥ 0.40 | aggressive ≥ 0.40 | relaxed |
| Background Mix | relaxed ≥ 0.35 | aggressive ≥ 0.50, party ≥ 0.55 | relaxed |

Genre exclusions are applied on top of all mood conditions — see [Genre Exclusions](#genre-exclusions) below.

## Documentation

For detailed setup instructions, full configuration reference, troubleshooting, and monitoring guidance, see **[HELP.md](HELP.md)**.

## Quick Start

### 1. Start the Analyzer Service
The plugin needs an external service to perform audio analysis (essentia can't run inside WASM). A ready-to-use **multi-arch** Docker image (supporting amd64 and arm64/Raspberry Pi) is provided.

**Important for ARM64 Users:** Since there are no pre-built `essentia-tensorflow` wheels for ARM64, the Dockerfile will compile Essentia from source. It is highly recommended to **build the image natively on your device** rather than using emulated multi-arch builds, which are extremely slow and prone to memory issues.

```bash
cd analyzer-service
# On Raspberry Pi, this will compile Essentia from source (~15-20 mins on Pi 5)
docker build --build-arg TARGETARCH=arm64 -t mood-analyzer .
```

Or add it to your existing `docker-compose.yml`:

```yaml
mood-analyzer:
  build:
    context: ./analyzer-service
  container_name: mood-analyzer
  restart: unless-stopped
```

Or use the published image from the latest release:

```yaml
mood-analyzer:
  image: ghcr.io/rflundgren/navidrome-mood-plugin:latest
  container_name: mood-analyzer
  restart: unless-stopped
```

### 2. Install the Plugin

1. Download `mood-playlists.ndp` from [Releases](https://github.com/RFLundgren/navidrome-mood-plugin/releases) (or [build from source](#building-from-source))
2. Copy it to your Navidrome plugins directory: `<navidrome-data>/plugins/`
3. Restart Navidrome (or it auto-loads if `ND_PLUGINS_AUTORELOAD=true`)
4. Go to **Settings > Plugins > Mood Playlists** and approve permissions
5. Set the **Analyzer Service URL** to your analyzer (e.g., `http://mood-analyzer:8000`)

### 3. Configure Agent Precedence (For Instant Mix)

If you are using multiple Navidrome metadata agents (like AudioMuse-AI, Last.fm, etc.), you must tell Navidrome to query the Mood plugin first for Instant Mix to work correctly.

Add the `ND_AGENTS` environment variable to your Navidrome configuration and list `mood-playlists` **before** other similar-song providers:

```yaml
ND_AGENTS: mood-playlists,audiomuseai,lastfm,listenbrainz
```

### 4. Done

The plugin will:
- Analyze unanalyzed tracks daily at 2 AM (configurable)
- Refresh all 13 mood playlists weekly on Sunday at 3 AM (configurable)
- Return mood-similar tracks when you use Instant Mix on any analyzed track

## Requirements

- **Navidrome** `develop` branch (plugin support requires 0.61.0+)
- **Docker** (for the analyzer service)
- Navidrome config:
  ```yaml
  ND_PLUGINS_ENABLED: "true"
  ND_PLUGINS_AUTORELOAD: "true"  # optional but recommended
  ```

## Analyzer Service

The analyzer service (`analyzer-service/`) is a lightweight FastAPI app that wraps essentia-tensorflow. It exposes a single endpoint:

```
POST /api/analysis/file
Content-Type: application/json

{"file_path": "/music/Artist/Album/Track.m4a"}
```

Response:
```json
{
  "file_path": "/music/Artist/Album/Track.m4a",
  "title": "Track Name",
  "artist": "Artist Name",
  "album": "Album Name",
  "bpm": 120.0,
  "danceability": 0.75,
  "mood_happy": 0.82,
  "mood_sad": 0.15,
  "mood_relaxed": 0.45,
  "mood_aggressive": 0.10,
  "mood_party": 0.68,
  "energy": 0.55
}
```

The Docker image (~500MB) includes:
- `essentia-tensorflow` with pre-trained Discogs-EffNet embedding model
- 6 mood classification heads (happy, sad, relaxed, aggressive, party) + danceability
- BPM and energy extraction
- Genre/BPM-aware context boosts (see below)

You can also use any custom service that implements this API.

### Context-Aware Scoring

Raw essentia scores are adjusted using track metadata for better accuracy. Without this, genres like Drum & Bass score near-zero on danceability despite being inherently danceable.

**Genre boosts** — 25+ genre keywords nudge scores based on the track's genre tag:

| Genre | Adjustments |
|-------|-------------|
| DnB / Jungle / Drum & Bass | danceability +0.35, party +0.15, aggressive +0.10 |
| Dance / House | danceability +0.20, party +0.10 |
| Techno | danceability +0.25, party +0.15, aggressive +0.10 |
| Metal | aggressive +0.25, relaxed -0.15 |
| Ambient / Downtempo | relaxed +0.20, aggressive -0.10 |
| Disco / Funk | danceability +0.20, party +0.15, happy +0.10 |
| Pop | happy +0.05, danceability +0.05 |
| Blues / Emo | sad +0.10–0.15 |

**BPM correction** — DnB is often detected at half-time (86 BPM instead of 172). Tracks with 80–95 BPM in DnB/Jungle genres are corrected to double-time, which triggers a +0.20 danceability boost for the 140–180 BPM range.

## Genre Exclusions

The essentia models analyze audio texture — they detect tempo, spectral brightness, and dynamics, not cultural genre context. This means a slow, quiet metal track can legitimately score high on `mood_relaxed` and appear in the Chill or Sleep Mix, even though it is clearly not appropriate there.

Genre exclusions solve this with hard blocklists applied during playlist generation. Tracks whose genre tag matches any keyword in a mix's exclusion list are ineligible for that mix, regardless of their mood scores.

**Default exclusions:**

| Mix | Excluded genres (keyword match) |
|-----|---------------------------------|
| Chill | metal, hard rock, punk, hardcore, industrial, grunge, thrash |
| Sleep | metal, hard rock, punk, hardcore, industrial, grunge, thrash, dance, techno, trance, house, edm, drum and bass |
| Study | metal, punk, hardcore, industrial |
| Dining | metal, hard rock, punk, hardcore, industrial |
| Background | metal, hard rock, punk, hardcore, industrial |
| Road Trip | metal, hardcore, industrial |

Matching is case-insensitive substring — `metal` matches `Heavy Metal`, `Power Metal`, `Symphonic Metal`, etc.

**Customising exclusions:** Each mix has its own config field (`chill_excluded_genres`, `sleep_excluded_genres`, etc.) in the Genre Exclusions section of the plugin settings. Leave the field empty to use the defaults above. Enter a comma-separated list to override entirely.

**Genre migration:** If your library was analyzed before genre exclusions were introduced, existing KV entries may not have genre data. Enable **Run Genre Migration** in the plugin settings (Genre Exclusions section) to backfill genre data from Navidrome into all existing entries — no re-analysis required. Disable it again after it completes (check logs for `Genre migration complete`).

## Configuration

All settings are configurable from Navidrome's plugin settings UI:

| Setting | Default | Description |
|---------|---------|-------------|
| Analyzer Service URL | `http://mood-analyzer:8000` | URL of the mood analyzer HTTP service |
| Auto-Analyze | `true` | Automatically analyze new tracks on schedule |
| Analysis Schedule | `0 2 * * *` | Cron expression (default: 2 AM daily) |
| Re-analyze Uncertain | `true` | Re-queue tracks with low-confidence scores |
| Re-analyze Percent | `0` | % of library to randomly re-analyze each cycle (0–20) |
| Re-analysis Schedule | `0 4 1 * *` | Cron expression for dedicated re-analysis run |
| Playlist Refresh Schedule | `0 3 * * 0` | Cron expression (default: 3 AM Sundays) |
| Tracks per Playlist | `30` | Number of tracks in each mood playlist (applies to all 13) |
| Similar Songs Count | `20` | Tracks returned for Instant Mix |
| Max Tracks per Artist | `3` | Per-artist cap per playlist (0 = no limit) |
| Max Analysis Workers | `2` | Number of concurrent analysis tasks (1-8) |
| Playlist Variation Pool | `3` | Pool multiplier for weekly variation (1–10) |
| Happy Threshold | `0.55` | Minimum score (0-1) for happy classification |
| Chill Threshold | `0.40` | Minimum score for chill/relaxed |
| Energetic Threshold | `0.60` | Minimum score for energetic/danceable |
| Party Threshold | `0.55` | Minimum score for party |
| Melancholy Threshold | `0.45` | Minimum score for sad/melancholy |
| Aggressive Threshold | `0.55` | Minimum score for aggressive |
| Show Dates in Playlist Names | `true` | Append generation/creation dates directly to playlist titles |
| Add Creation Dates to Playlists | `false` | Automatically sync creation dates to all playlists |
| Creation Date Sync Schedule | `0 5 * * *` | Cron expression for the creation date sync task |
| Chill Excluded Genres | _(see defaults)_ | Comma-separated genre keywords blocked from Chill Mix |
| Sleep Excluded Genres | _(see defaults)_ | Comma-separated genre keywords blocked from Sleep Mix |
| Study Excluded Genres | _(see defaults)_ | Comma-separated genre keywords blocked from Study Mix |
| Dining Excluded Genres | _(see defaults)_ | Comma-separated genre keywords blocked from Dining Mix |
| Background Excluded Genres | _(see defaults)_ | Comma-separated genre keywords blocked from Background Mix |
| Road Trip Excluded Genres | _(see defaults)_ | Comma-separated genre keywords blocked from Road Trip Mix |
| Run Genre Migration | `false` | One-time backfill of genre data into existing analyzed tracks |
| Genre Migration Schedule | `0 1 * * *` | Cron expression for the genre migration pass |

Composite mood conditions (requires/excludes thresholds) are fixed in code and not configurable via the UI.

## How It Works

```
┌─────────────────────────────┐     ┌──────────────────────────┐
│     Navidrome (+ plugin)    │     │   mood-analyzer service   │
│                             │     │   (essentia-tensorflow)   │
│  mood-playlists.ndp         │────>│                           │
│  - scheduler: analyze new   │HTTP │  POST /api/analysis/file  │
│    tracks daily             │     │  -> mood scores + BPM     │
│  - kvstore: cache scores    │     │                           │
│  - subsonicapi: create      │     └──────────────────────────┘
│    playlists                │
│  - instant mix: mood-       │
│    similar tracks           │
└─────────────────────────────┘
```

1. **Analysis** — On schedule, the plugin iterates all tracks via Subsonic API, sends unanalyzed ones to the analyzer service, and stores mood scores in its KVStore. The analyzer extracts raw audio features via essentia-tensorflow, then applies genre/BPM context boosts so that genre-specific characteristics (like DnB's danceability) are properly reflected.

2. **Playlists** — On the refresh schedule, it queries stored mood data and creates two types of playlists:
   - **Simple moods** — selects tracks above a single threshold (sorted by that score)
   - **Composite moods** — selects tracks that pass both a minimum positive requirement and maximum caps on negative attributes, sorted by the primary score field
   - **Genre exclusions** — applied across all playlists after mood filtering; tracks whose genre tag matches a blocklist keyword are removed regardless of score

3. **Instant Mix** — When triggered on a track, calculates Euclidean distance between the source track's mood vector and all analyzed tracks, returning the closest matches.

4. **Metadata Sync** — Optionally adds creation dates to all non-plugin playlists, and tracks generation dates for mood playlists, exposing this metadata via the Subsonic API and optionally appending it to playlist titles.

## Building from Source

### 1. Analyzer Service (Multi-Arch)

The analyzer service Docker image is architecture-aware. When building from source, Docker will automatically detect your platform:

```bash
cd analyzer-service
docker build -t mood-analyzer .
```

- **x64 (AMD64):** Build is fast (< 1 min) as it uses pre-compiled wheels.
- **ARM64 (Raspberry Pi):** Build takes **15–20 minutes** on a Pi 5, as it installs build tools and compiles Essentia with TensorFlow support from source.

### 2. Plugin (WASM)

#### With TinyGo (recommended — smaller binary)

```bash
tinygo build -opt=2 -scheduler=none -no-debug -o plugin.wasm -target wasip1 -buildmode=c-shared .
```

**Windows (PowerShell):**
```powershell
Remove-Item mood-playlists.ndp -ErrorAction SilentlyContinue
Compress-Archive -Path plugin.wasm, manifest.json -DestinationPath mood-playlists.zip
Rename-Item mood-playlists.zip mood-playlists.ndp
```

**Linux / macOS:**
```bash
zip mood-playlists.ndp plugin.wasm manifest.json
```

#### With Go 1.26+

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
```

Then package as above.

## Contributing

Contributions welcome! Some ideas:

- [ ] Per-user mood playlists
- [ ] "Mood of the day" rotating playlist
- [ ] Configurable thresholds for composite moods via the settings UI
- [ ] Last.fm tag integration for cultural mood context (crowd-sourced tags to complement audio analysis)

## License

GPL-3.0 — same as Navidrome.

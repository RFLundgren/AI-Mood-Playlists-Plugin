// Package main implements a Navidrome plugin that builds and maintains mood-
// and genre-based playlists from tags written by the AI Auto-Tagging plugin
// (github.com/RFLundgren/AI-auto-tagging-plugin), and powers Instant Mix via
// shared-tag overlap.
package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lifecycle"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"
)

// ── Types ────────────────────────────────────────────────────────

type taggedTrack struct {
	ID     string
	Title  string
	Artist string
}

const rebuildQueue = "mood-rebuild"

const (
	rebuildPayload = "rebuild"
	enrichPayload  = "enrich-playlists"
)

// ── Plugin Registration ──────────────────────────────────────────

type moodPlugin struct{}

func init() {
	p := &moodPlugin{}
	lifecycle.Register(p)
	scheduler.Register(p)
	taskworker.Register(p)
	metadata.Register(p)
}

func main() {}

// ── Initialization ───────────────────────────────────────────────

func (p *moodPlugin) OnInit() error {
	pdk.Log(pdk.LogInfo, "AI Mood Playlists plugin initializing...")

	rebuildSchedule := configString("rebuild_schedule", "0 * * * *")
	if _, err := host.SchedulerScheduleRecurring(rebuildSchedule, rebuildPayload, "mood-rebuild-schedule"); err != nil {
		return fmt.Errorf("scheduling rebuild: %w", err)
	}
	pdk.Log(pdk.LogInfo, "Scheduled playlist rebuild: "+rebuildSchedule)

	if configBool("enrich_playlists", false) {
		schedule := configString("enrich_schedule", "0 5 * * *")
		if _, err := host.SchedulerScheduleRecurring(schedule, enrichPayload, "mood-enrich"); err != nil {
			pdk.Log(pdk.LogError, "Failed to schedule metadata enrichment: "+err.Error())
		} else {
			pdk.Log(pdk.LogInfo, "Scheduled metadata enrichment task: "+schedule)
		}
	}

	// Clear any stale tasks from previous runs, then ensure the queue exists.
	host.TaskClearQueue(rebuildQueue)
	concurrency := configInt("max_concurrency", 2)
	if err := host.TaskCreateQueue(rebuildQueue, host.QueueConfig{
		Concurrency: int32(concurrency),
		MaxRetries:  3,
		BackoffMs:   10_000,
	}); err != nil {
		pdk.Log(pdk.LogDebug, "Task queue init: "+err.Error())
	}

	pdk.Log(pdk.LogInfo, "AI Mood Playlists plugin initialized")
	return nil
}

// ── Scheduled Task Handler ───────────────────────────────────────

func (p *moodPlugin) OnCallback(req scheduler.SchedulerCallbackRequest) error {
	switch req.Payload {
	case rebuildPayload:
		return startRebuild()
	case enrichPayload:
		return enrichPlaylists()
	default:
		pdk.Log(pdk.LogWarn, "Unknown schedule payload: "+req.Payload)
		return nil
	}
}

// ── Similar Songs (Instant Mix) ──────────────────────────────────

// GetSimilarSongsByTrack ranks candidates by how many tags they share with
// the source track (see PLAN.md Decision 1 - reimagined around shared-tag
// overlap since discrete AI tags don't support continuous-vector distance).
func (p *moodPlugin) GetSimilarSongsByTrack(req metadata.SimilarSongsByTrackRequest) (*metadata.SimilarSongsResponse, error) {
	count := int(req.Count)
	if count <= 0 {
		count = configInt("similar_songs_count", 20)
	}

	sourceTags, err := fetchUserTags(req.ID)
	if err != nil || len(sourceTags) == 0 {
		return &metadata.SimilarSongsResponse{}, nil
	}

	overlap := make(map[string]int)
	info := make(map[string]taggedTrack)
	for _, tag := range sourceTags {
		tracks, err := fetchSongsByTag(tag)
		if err != nil {
			continue
		}
		for _, t := range tracks {
			if t.ID == req.ID {
				continue
			}
			overlap[t.ID]++
			info[t.ID] = t
		}
	}

	type candidate struct {
		track   taggedTrack
		overlap int
	}
	candidates := make([]candidate, 0, len(overlap))
	for id, n := range overlap {
		candidates = append(candidates, candidate{track: info[id], overlap: n})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].overlap > candidates[j].overlap })

	if count > len(candidates) {
		count = len(candidates)
	}
	songs := make([]metadata.SongRef, count)
	for i := 0; i < count; i++ {
		songs[i] = metadata.SongRef{
			ID:     candidates[i].track.ID,
			Name:   candidates[i].track.Title,
			Artist: candidates[i].track.Artist,
		}
	}
	return &metadata.SimilarSongsResponse{Songs: songs}, nil
}

// ── Playlist Rebuild ─────────────────────────────────────────────

// startRebuild is the scheduler entry point. It discovers every tag AI
// Auto-Tagging has written, filters to the configured categories, and
// enqueues one rebuild task per tag value - kept fast enough to stay well
// under Navidrome's 30-second scheduler-callback limit; the actual per-tag
// work happens in the task queue via rebuildTagPlaylist.
func startRebuild() error {
	pdk.Log(pdk.LogInfo, "Starting playlist rebuild...")

	tags, err := fetchAllUserTags()
	if err != nil {
		return fmt.Errorf("getAllUserTags failed: %w", err)
	}

	categories := parseList(configString("playlist_tag_categories", "genre,mood"))
	// Per-category allowlists are the definitive list of which values in that
	// category get a playlist - pre-filled by default with every value in
	// AI Auto-Tagging's own default vocabulary, so a fresh install still
	// builds a playlist per value out of the box, but editable down to
	// exactly what's wanted. Clearing a list entirely means "no playlists
	// for this category", not "allow everything" - see configVocabularyList.
	genreAllowlist := configVocabularyList("genre_allowlist", defaultGenreVocabulary)
	moodAllowlist := configVocabularyList("mood_allowlist", defaultMoodVocabulary)
	queued := 0
	for _, tag := range tags {
		category, value, ok := strings.Cut(tag, ":")
		if !ok || !slices.Contains(categories, category) {
			continue
		}
		switch category {
		case "genre":
			if !slices.Contains(genreAllowlist, value) {
				continue
			}
		case "mood":
			if !slices.Contains(moodAllowlist, value) {
				continue
			}
		}
		taskData, _ := json.Marshal(rebuildTask{Tag: tag})
		if _, err := host.TaskEnqueue(rebuildQueue, taskData); err != nil {
			pdk.Log(pdk.LogWarn, "Failed to queue rebuild for "+tag+": "+err.Error())
			continue
		}
		queued++
	}

	// User-defined combination playlists (e.g. "Workout Rock" = genre rock/
	// metal AND mood energetic/aggressive) - see parseCustomPlaylists for the
	// config syntax. Independent of the allowlists/categories above, since a
	// combination playlist is opt-in and named by the user, not discovered.
	for _, def := range parseCustomPlaylists(configString("custom_playlists", "")) {
		taskData, _ := json.Marshal(rebuildTask{
			Name:         def.Name,
			Genres:       def.Genres,
			Moods:        def.Moods,
			Size:         def.Size,
			ArtistCap:    def.ArtistCap,
			ArtistCapSet: def.ArtistCapSet,
		})
		if _, err := host.TaskEnqueue(rebuildQueue, taskData); err != nil {
			pdk.Log(pdk.LogWarn, "Failed to queue custom playlist '"+def.Name+"': "+err.Error())
			continue
		}
		queued++
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Queued %d playlist(s) for rebuild", queued))
	return nil
}

// rebuildTask is the task-queue payload for both discovered tag-value
// playlists (Tag set) and user-defined combination playlists (Name set) -
// exactly one of the two is populated for any given task.
type rebuildTask struct {
	Tag string `json:"tag,omitempty"`

	Name         string   `json:"name,omitempty"`
	Genres       []string `json:"genres,omitempty"`
	Moods        []string `json:"moods,omitempty"`
	Size         int      `json:"size,omitempty"`
	ArtistCap    int      `json:"artist_cap,omitempty"`
	ArtistCapSet bool     `json:"artist_cap_set,omitempty"`
}

func (p *moodPlugin) OnTaskExecute(req taskworker.TaskExecuteRequest) (string, error) {
	var task rebuildTask
	if err := json.Unmarshal(req.Payload, &task); err != nil {
		return "", fmt.Errorf("decoding task payload: %w", err)
	}

	switch {
	case task.Tag != "":
		if err := rebuildTagPlaylist(task.Tag); err != nil {
			return "", err
		}
		return "rebuilt " + task.Tag, nil
	case task.Name != "":
		def := customPlaylistDef{
			Name:         task.Name,
			Genres:       task.Genres,
			Moods:        task.Moods,
			Size:         task.Size,
			ArtistCap:    task.ArtistCap,
			ArtistCapSet: task.ArtistCapSet,
		}
		if err := rebuildCustomPlaylist(def); err != nil {
			return "", err
		}
		return "rebuilt custom playlist " + task.Name, nil
	default:
		return "", fmt.Errorf("task payload has neither tag nor name")
	}
}

// rebuildTagPlaylist gathers every track carrying tag, shuffles, applies the
// per-artist cap and playlist size, then upserts the resulting playlist. See
// PLAN.md Decision 2 - simpler and run far more often than the original's
// weekly cadence, since reading tags is cheap compared to the audio analysis
// that justified that cadence before.
func rebuildTagPlaylist(tag string) error {
	tracks, err := fetchSongsByTag(tag)
	if err != nil {
		return fmt.Errorf("getSongsByUserTag failed: %w", err)
	}
	if len(tracks) == 0 {
		pdk.Log(pdk.LogWarn, "No tracks found for tag "+tag)
		return nil
	}

	playlistSize := configInt("playlist_size", 50)
	maxPerArtist := configInt("max_tracks_per_artist", 3)
	songIDs := selectTracks(tracks, playlistSize, maxPerArtist)
	if len(songIDs) == 0 {
		pdk.Log(pdk.LogWarn, "No tracks selected for tag "+tag+" after artist-cap filtering")
		return nil
	}

	existingIDs := getExistingPlaylistIDs()
	upsertPlaylist(tagLabel(tag), songIDs, existingIDs)
	return nil
}

// ── Custom combination playlists ─────────────────────────────────

// customPlaylistDef describes one user-defined playlist built from a
// combination of genre and/or mood tag values - see parseCustomPlaylists for
// the config syntax that produces these.
type customPlaylistDef struct {
	Name   string
	Genres []string
	Moods  []string
	// Size overrides playlist_size for this playlist only; 0 means "not
	// specified, use the global default".
	Size int
	// ArtistCap overrides max_tracks_per_artist for this playlist only.
	// ArtistCapSet distinguishes "not specified" (use the global default)
	// from an explicit 0 (no cap), the same way configVocabularyList
	// distinguishes "unset" from "explicitly empty".
	ArtistCap    int
	ArtistCapSet bool
}

// parseCustomPlaylists parses the "custom_playlists" config field. Entries
// are separated by a newline or a ";" (both are accepted since it's
// unclear whether the config UI renders this field as a single-line box or
// a multi-line one), each in the form:
//
//	Name: genre=value1,value2 | mood=value1,value2 | size=40 | artist_cap=2
//
// genre/mood are each optional, but at least one must be present with a
// non-empty value list or the whole entry is skipped (a playlist needs
// something to match tracks on). size/artist_cap are optional per-playlist
// overrides of the plugin-wide playlist_size/max_tracks_per_artist settings.
func parseCustomPlaylists(raw string) []customPlaylistDef {
	raw = strings.ReplaceAll(raw, ";", "\n")
	var defs []customPlaylistDef
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}

		def := customPlaylistDef{Name: name}
		for _, field := range strings.Split(rest, "|") {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			key = strings.ToLower(strings.TrimSpace(key))
			value = strings.TrimSpace(value)
			switch key {
			case "genre", "genres":
				def.Genres = parseList(value)
			case "mood", "moods":
				def.Moods = parseList(value)
			case "size":
				if n, err := strconv.Atoi(value); err == nil && n > 0 {
					def.Size = n
				}
			case "artist_cap", "artistcap", "cap":
				if n, err := strconv.Atoi(value); err == nil && n >= 0 {
					def.ArtistCap = n
					def.ArtistCapSet = true
				}
			}
		}
		if len(def.Genres) == 0 && len(def.Moods) == 0 {
			continue
		}
		defs = append(defs, def)
	}
	return defs
}

// rebuildCustomPlaylist gathers every track matching def's genre/mood
// combination (a track qualifies if it carries at least one of the listed
// values in each category that was specified - categories left empty in the
// definition impose no constraint), then selects and upserts a playlist the
// same way rebuildTagPlaylist does for a single tag.
func rebuildCustomPlaylist(def customPlaylistDef) error {
	var genreTracks, moodTracks map[string]taggedTrack
	var err error
	if len(def.Genres) > 0 {
		if genreTracks, err = fetchTracksForCategory("genre", def.Genres); err != nil {
			return fmt.Errorf("fetching genre tracks for '%s': %w", def.Name, err)
		}
	}
	if len(def.Moods) > 0 {
		if moodTracks, err = fetchTracksForCategory("mood", def.Moods); err != nil {
			return fmt.Errorf("fetching mood tracks for '%s': %w", def.Name, err)
		}
	}

	var candidates []taggedTrack
	switch {
	case len(def.Genres) > 0 && len(def.Moods) > 0:
		for id, t := range genreTracks {
			if _, ok := moodTracks[id]; ok {
				candidates = append(candidates, t)
			}
		}
	case len(def.Genres) > 0:
		for _, t := range genreTracks {
			candidates = append(candidates, t)
		}
	default:
		for _, t := range moodTracks {
			candidates = append(candidates, t)
		}
	}

	if len(candidates) == 0 {
		pdk.Log(pdk.LogWarn, "No tracks found for custom playlist '"+def.Name+"'")
		return nil
	}

	playlistSize := configInt("playlist_size", 50)
	if def.Size > 0 {
		playlistSize = def.Size
	}
	maxPerArtist := configInt("max_tracks_per_artist", 3)
	if def.ArtistCapSet {
		maxPerArtist = def.ArtistCap
	}

	songIDs := selectTracks(candidates, playlistSize, maxPerArtist)
	if len(songIDs) == 0 {
		pdk.Log(pdk.LogWarn, "No tracks selected for custom playlist '"+def.Name+"' after artist-cap filtering")
		return nil
	}

	existingIDs := getExistingPlaylistIDs()
	upsertPlaylist(playlistPrefix()+def.Name, songIDs, existingIDs)
	return nil
}

// fetchTracksForCategory fetches and merges tracks across every "category:
// value" tag in values, deduplicating by track ID (a track matching more
// than one listed value in the same category should only count once).
func fetchTracksForCategory(category string, values []string) (map[string]taggedTrack, error) {
	result := map[string]taggedTrack{}
	for _, v := range values {
		tracks, err := fetchSongsByTag(category + ":" + v)
		if err != nil {
			return nil, err
		}
		for _, t := range tracks {
			result[t.ID] = t
		}
	}
	return result, nil
}

// selectTracks shuffles candidates, then walks the shuffled list applying
// the per-artist cap and duplicate-title/artist dedup (the same recording
// appearing on multiple albums should only appear once), stopping once limit
// is reached.
func selectTracks(candidates []taggedTrack, limit, maxPerArtist int) []string {
	shuffled := make([]taggedTrack, len(candidates))
	copy(shuffled, candidates)
	rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	artistCount := make(map[string]int)
	seen := make(map[string]bool)
	var ids []string
	for _, t := range shuffled {
		if len(ids) >= limit {
			break
		}
		artist := strings.ToLower(strings.TrimSpace(t.Artist))
		if artist == "" {
			artist = "__unknown__"
		}
		title := strings.ToLower(strings.TrimSpace(t.Title))
		dupKey := title + "\x00" + artist
		if seen[dupKey] {
			continue
		}
		if maxPerArtist > 0 && artistCount[artist] >= maxPerArtist {
			continue
		}
		ids = append(ids, t.ID)
		artistCount[artist]++
		seen[dupKey] = true
	}
	return ids
}

const defaultPlaylistPrefix = "AI "

// playlistPrefix returns the configured prefix for auto-generated playlist
// names, distinguishing them from any other plugin's (e.g. the audio-
// analysis-based Mood Playlists this was forked from uses plain names like
// "Happy Mix" - without a prefix, upsertPlaylist's name-prefix matching would
// find and silently overwrite those instead of creating a separate
// playlist). configString already falls back to the default below when the
// config value is blank, so an empty prefix - which would re-introduce
// exactly that collision risk - isn't reachable through normal config.
func playlistPrefix() string {
	return configString("playlist_name_prefix", defaultPlaylistPrefix)
}

// tagLabel derives a display name from a "category:value" tag, e.g.
// "mood:chill" -> "AI Chill Mix", "genre:new age" -> "AI New Age" (with the
// default prefix - see playlistPrefix).
func tagLabel(tag string) string {
	category, value, ok := strings.Cut(tag, ":")
	if !ok {
		value = tag
	}
	label := playlistPrefix() + titleCase(value)
	if category == "mood" {
		label += " Mix"
	}
	return label
}

func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

func parseList(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.ToLower(strings.TrimSpace(p)); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// ── Subsonic tag queries ─────────────────────────────────────────

func fetchAllUserTags() ([]string, error) {
	body, err := subsonicCall("getAllUserTags.view?")
	if err != nil {
		return nil, err
	}
	var resp struct {
		SubsonicResponse struct {
			UserTags struct {
				Tag []string `json:"tag"`
			} `json:"userTags"`
		} `json:"subsonic-response"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, fmt.Errorf("parsing getAllUserTags response: %w", err)
	}
	return resp.SubsonicResponse.UserTags.Tag, nil
}

func fetchUserTags(trackID string) ([]string, error) {
	body, err := subsonicCall("getUserTags.view?id=" + url.QueryEscape(trackID))
	if err != nil {
		return nil, err
	}
	var resp struct {
		SubsonicResponse struct {
			UserTags struct {
				Tag []string `json:"tag"`
			} `json:"userTags"`
		} `json:"subsonic-response"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, fmt.Errorf("parsing getUserTags response: %w", err)
	}
	return resp.SubsonicResponse.UserTags.Tag, nil
}

func fetchSongsByTag(tag string) ([]taggedTrack, error) {
	body, err := subsonicCall("getSongsByUserTag.view?tag=" + url.QueryEscape(tag))
	if err != nil {
		return nil, err
	}
	var resp struct {
		SubsonicResponse struct {
			SongsByUserTag struct {
				Song []struct {
					ID     string `json:"id"`
					Title  string `json:"title"`
					Artist string `json:"artist"`
				} `json:"song"`
			} `json:"songsByUserTag"`
		} `json:"subsonic-response"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, fmt.Errorf("parsing getSongsByUserTag response: %w", err)
	}
	tracks := make([]taggedTrack, 0, len(resp.SubsonicResponse.SongsByUserTag.Song))
	for _, s := range resp.SubsonicResponse.SongsByUserTag.Song {
		tracks = append(tracks, taggedTrack{ID: s.ID, Title: s.Title, Artist: s.Artist})
	}
	return tracks, nil
}

// ── Playlist upsert ──────────────────────────────────────────────

// getExistingPlaylistIDs returns a map of playlist name -> id for all
// playlists visible to the configured user. Used by upsertPlaylist to update
// rather than duplicate when a tag's playlist already exists.
func getExistingPlaylistIDs() map[string]string {
	result := map[string]string{}
	body, err := subsonicCall("getPlaylists.view?")
	if err != nil {
		pdk.Log(pdk.LogWarn, "getPlaylists failed: "+err.Error())
		return result
	}
	var resp struct {
		SubsonicResponse struct {
			Playlists struct {
				Playlist []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"playlist"`
			} `json:"playlists"`
		} `json:"subsonic-response"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		pdk.Log(pdk.LogWarn, "Failed to parse getPlaylists response: "+err.Error())
		return result
	}
	for _, pl := range resp.SubsonicResponse.Playlists.Playlist {
		result[pl.Name] = pl.ID
	}
	return result
}

// upsertPlaylist creates a new playlist or replaces the tracks in an
// existing one, matched by name prefix, and stamps a "last generated"
// timestamp into the title/comment per the show_dates_in_title setting.
func upsertPlaylist(baseLabel string, songIDs []string, existingIDs map[string]string) {
	now := time.Now().Format("02 Jan, 15:04")

	fullName := baseLabel
	if configBool("show_dates_in_title", true) {
		fullName = fmt.Sprintf("%s (%s)", baseLabel, now)
	}
	commentStr := "Last generated: " + now

	var plID string
	for name, id := range existingIDs {
		if strings.HasPrefix(name, baseLabel) {
			plID = id
			break
		}
	}

	var params string
	isUpdate := plID != ""
	if isUpdate {
		// Navidrome's createPlaylist ignores "name" when playlistId is set
		// (Create() ignores it and reuses the existing name) - the follow-up
		// updatePlaylist call below is what actually renames it, via "name".
		params = "playlistId=" + url.QueryEscape(plID)
	} else {
		params = "name=" + url.QueryEscape(fullName)
	}
	for _, id := range songIDs {
		params += "&songId=" + url.QueryEscape(id)
	}

	if _, err := subsonicCall("createPlaylist?" + params); err != nil {
		pdk.Log(pdk.LogError, "Failed to upsert playlist '"+fullName+"': "+err.Error())
		return
	}

	if !isUpdate {
		for name, id := range getExistingPlaylistIDs() {
			if strings.HasPrefix(name, baseLabel) {
				plID = id
				break
			}
		}
	}

	if plID != "" {
		updateParams := "playlistId=" + url.QueryEscape(plID) + "&name=" + url.QueryEscape(fullName) +
			"&comment=" + url.QueryEscape(commentStr)
		subsonicCall("updatePlaylist?" + updateParams)
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Updated playlist '%s' (%d tracks)", fullName, len(songIDs)))
}

// ── Subsonic HTTP helper (for scheduler/task-queue context where no user is injected) ──

func subsonicCall(uri string) (string, error) {
	ndURL := configString("navidrome_url", "http://navidrome:4533")
	user := configString("navidrome_user", "")
	pass := configString("navidrome_password", "")

	if user == "" {
		// No credentials configured - fall back to host call (works in user context)
		return host.SubsonicAPICall(uri)
	}

	sep := "?"
	if strings.Contains(uri, "?") {
		sep = "&"
	}
	fullURL := fmt.Sprintf("%s/rest/%s%su=%s&p=%s&v=1.16.1&c=ai-mood-playlists&f=json",
		ndURL, uri, sep, url.QueryEscape(user), url.QueryEscape(pass))

	resp, err := host.HTTPSend(host.HTTPRequest{
		URL:       fullURL,
		Method:    "GET",
		TimeoutMs: 30000,
	})
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return string(resp.Body), nil
}

// ── Config helpers ───────────────────────────────────────────────

func configString(key, defaultVal string) string {
	val, ok := host.ConfigGet(key)
	if !ok || val == "" {
		return defaultVal
	}
	return val
}

func configInt(key string, defaultVal int) int {
	val, ok := host.ConfigGetInt(key)
	if !ok {
		return defaultVal
	}
	return int(val)
}

func configBool(key string, defaultVal bool) bool {
	val, ok := host.ConfigGet(key)
	if !ok || val == "" {
		return defaultVal
	}
	return val == "true" || val == "1" || val == "yes"
}

// Pre-fills the genre_allowlist/mood_allowlist config fields so they show up
// fully populated (matching AI Auto-Tagging's own vocabulary defaults) for
// the user to edit down, rather than blank. See configVocabularyList.
const defaultGenreVocabulary = "rock, pop, electronic, hip hop, jazz, classical, metal, folk, country, r&b, " +
	"soul, blues, reggae, punk, indie, ambient, new age, world, funk, disco, house, techno, alternative, " +
	"soundtrack, experimental"

const defaultMoodVocabulary = "happy, chill, energetic, melancholy, party, aggressive, romantic, dreamy, " +
	"dark, uplifting, nostalgic, peaceful"

// configVocabularyList reads a comma-separated config value, distinguishing
// "never configured" (the config key is entirely absent, e.g. before the
// plugin's config has ever been saved - falls back to defaultCSV, so a fresh
// install shows the full pre-filled list and behaves as "every value
// allowed") from "explicitly cleared" (the key exists but is an empty
// string, meaning the user deliberately emptied the field - returns an empty
// list, meaning "no values allowed", i.e. build no playlists for this
// category). Once saved at all, an empty field is respected as deliberate,
// never silently re-defaulted.
func configVocabularyList(key, defaultCSV string) []string {
	raw, ok := host.ConfigGet(key)
	if !ok {
		return parseList(defaultCSV)
	}
	return parseList(raw)
}

// ── Playlist metadata enrichment ─────────────────────────────────

// enrichPlaylists iterates through all playlists in the library and appends
// a "Created: ..." tag to their comments (and optionally titles) if not
// already present. Unrelated to tagging/classification - kept as-is.
func enrichPlaylists() error {
	pdk.Log(pdk.LogInfo, "Running metadata enrichment for all playlists...")
	body, err := subsonicCall("getPlaylists.view?")
	if err != nil {
		return fmt.Errorf("getPlaylists failed: %w", err)
	}

	var resp struct {
		SubsonicResponse struct {
			Playlists struct {
				Playlist []struct {
					ID      string `json:"id"`
					Name    string `json:"name"`
					Comment string `json:"comment"`
					Created string `json:"created"`
					Owner   string `json:"owner"`
				} `json:"playlist"`
			} `json:"playlists"`
		} `json:"subsonic-response"`
	}

	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return fmt.Errorf("failed to parse playlists: %w", err)
	}

	currentUser := configString("navidrome_user", "")
	showInTitle := configBool("show_dates_in_title", true)
	enrichedCount := 0

	for _, pl := range resp.SubsonicResponse.Playlists.Playlist {
		if currentUser != "" && pl.Owner != "" && !strings.EqualFold(pl.Owner, currentUser) {
			continue
		}

		// Skip tag-generated playlists — they manage their own dates.
		if strings.Contains(pl.Comment, "Last generated:") || strings.Contains(pl.Name, " Mix (") ||
			strings.HasSuffix(pl.Name, " Mix") {
			continue
		}

		hasTitleTag := strings.Contains(pl.Name, "(Created:")
		hasCommentTag := strings.Contains(pl.Comment, "Created:")

		needsTitleUpdate := (showInTitle && !hasTitleTag) || (!showInTitle && hasTitleTag)
		needsCommentUpdate := !hasCommentTag

		if !needsTitleUpdate && !needsCommentUpdate {
			continue
		}

		createdDate := pl.Created
		if t, err := time.Parse(time.RFC3339, pl.Created); err == nil {
			createdDate = t.Format("02 Jan, 15:04")
		} else if len(pl.Created) >= 10 {
			createdDate = pl.Created[:10]
		}

		newName := pl.Name
		if showInTitle && !hasTitleTag {
			newName = fmt.Sprintf("%s (Created: %s)", pl.Name, createdDate)
		} else if !showInTitle && hasTitleTag {
			if idx := strings.LastIndex(pl.Name, " (Created:"); idx != -1 {
				newName = strings.TrimSpace(pl.Name[:idx])
			}
		}

		newComment := pl.Comment
		if needsCommentUpdate {
			tag := "Created: " + createdDate
			if newComment == "" {
				newComment = tag
			} else {
				newComment = newComment + "\n" + tag
			}
		}

		params := "playlistId=" + url.QueryEscape(pl.ID) + "&name=" + url.QueryEscape(newName) +
			"&comment=" + url.QueryEscape(newComment)
		if _, err := subsonicCall("updatePlaylist?" + params); err != nil {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("Failed to update playlist '%s': %s", pl.Name, err.Error()))
			continue
		}
		enrichedCount++
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Metadata enrichment complete. Updated %d playlists.", enrichedCount))
	return nil
}

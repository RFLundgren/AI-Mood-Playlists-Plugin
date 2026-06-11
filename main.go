// Package main implements a Navidrome plugin that provides mood-based playlists
// and similar song recommendations using audio analysis from an external service.
//
//go:build wasip1

package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

// ── Types ────────────────────────────────────────────────────────

type MoodScores struct {
	MoodHappy      float64 `json:"mood_happy"`
	MoodSad        float64 `json:"mood_sad"`
	MoodRelaxed    float64 `json:"mood_relaxed"`
	MoodAggressive float64 `json:"mood_aggressive"`
	MoodParty      float64 `json:"mood_party"`
	Danceability   float64 `json:"danceability"`
	BPM            float64 `json:"bpm"`
	Energy         float64 `json:"energy"`
	Arousal        float64 `json:"arousal"`
	Valence        float64 `json:"valence"`
	Genre          string  `json:"genre,omitempty"`
}

type AnalysisResponse struct {
	FilePath       string  `json:"file_path"`
	Title          string  `json:"title"`
	Artist         string  `json:"artist"`
	Album          string  `json:"album"`
	BPM            float64 `json:"bpm"`
	Danceability   float64 `json:"danceability"`
	MoodHappy      float64 `json:"mood_happy"`
	MoodSad        float64 `json:"mood_sad"`
	MoodRelaxed    float64 `json:"mood_relaxed"`
	MoodAggressive float64 `json:"mood_aggressive"`
	MoodParty      float64 `json:"mood_party"`
	Energy         float64 `json:"energy"`
	Arousal        float64 `json:"arousal"`
	Valence        float64 `json:"valence"`
}

type trackWithScores struct {
	ID     string     `json:"id"`
	Name   string     `json:"name,omitempty"`
	Artist string     `json:"artist,omitempty"`
	Scores MoodScores `json:"scores"`
}

// Simple mood: single score field >= threshold, with optional exclusion conditions.
// A track is excluded if it matches ANY condition in Exclude.
type MoodDefinition struct {
	Key           string
	Label         string
	ScoreField    string
	ThresholdKey  string
	DefaultThresh float64
	Exclude       []Condition
}

// Composite mood: multiple conditions that must ALL be true
type Condition struct {
	Field string  // score field name (mood_happy, bpm, energy, etc.)
	Op    string  // ">=" or "<"
	Value float64 // threshold value
}

type CompositeMoodDefinition struct {
	Key        string
	Label      string
	Conditions []Condition
	SortField  string // which field to sort by (descending)
}

var moods = []MoodDefinition{
	{Key: "happy", Label: "Happy Mix", ScoreField: "mood_happy", ThresholdKey: "happy_threshold", DefaultThresh: 0.55,
		Exclude: []Condition{
			{Field: "mood_sad", Op: ">=", Value: 0.4},
		}},
	{Key: "chill", Label: "Chill Mix", ScoreField: "mood_relaxed", ThresholdKey: "chill_threshold", DefaultThresh: 0.55,
		Exclude: []Condition{
			{Field: "mood_aggressive", Op: ">=", Value: 0.35},
		}},
	{Key: "energetic", Label: "Energetic Mix", ScoreField: "danceability", ThresholdKey: "energetic_threshold", DefaultThresh: 0.6},
	{Key: "melancholy", Label: "Melancholy Mix", ScoreField: "mood_sad", ThresholdKey: "melancholy_threshold", DefaultThresh: 0.45,
		Exclude: []Condition{
			{Field: "mood_happy", Op: ">=", Value: 0.5},
		}},
	{Key: "party", Label: "Party Mix", ScoreField: "mood_party", ThresholdKey: "party_threshold", DefaultThresh: 0.55,
		Exclude: []Condition{
			{Field: "mood_sad", Op: ">=", Value: 0.4},
		}},
	{Key: "aggressive", Label: "Aggressive Mix", ScoreField: "mood_aggressive", ThresholdKey: "aggressive_threshold", DefaultThresh: 0.55,
		Exclude: []Condition{
			{Field: "mood_relaxed", Op: ">=", Value: 0.35},
			{Field: "mood_happy", Op: ">=", Value: 0.45},
		}},
}

var compositeMoods = []CompositeMoodDefinition{
	{
		// Calm, focused — requires relaxed baseline; exclude aggressive and party tracks
		Key:   "study",
		Label: "Study Mix",
		Conditions: []Condition{
			{Field: "mood_relaxed", Op: ">=", Value: 0.40},
			{Field: "mood_aggressive", Op: "<", Value: 0.45},
			{Field: "mood_party", Op: "<", Value: 0.50},
		},
		SortField: "mood_relaxed",
	},
	{
		// High-energy movement — requires danceability; exclude slow/sad tracks
		Key:   "workout",
		Label: "Workout Mix",
		Conditions: []Condition{
			{Field: "danceability", Op: ">=", Value: 0.50},
			{Field: "mood_relaxed", Op: "<", Value: 0.60},
			{Field: "mood_sad", Op: "<", Value: 0.50},
		},
		SortField: "danceability",
	},
	{
		// Very quiet and calm — requires relaxed baseline; strict aggression/party ceiling
		Key:   "sleep",
		Label: "Sleep Mix",
		Conditions: []Condition{
			{Field: "mood_relaxed", Op: ">=", Value: 0.30},
			{Field: "mood_aggressive", Op: "<", Value: 0.30},
			{Field: "mood_party", Op: "<", Value: 0.35},
		},
		SortField: "mood_relaxed",
	},
	{
		// Upbeat driving — requires happy baseline; exclude aggressive and sad
		Key:   "road_trip",
		Label: "Road Trip Mix",
		Conditions: []Condition{
			{Field: "mood_happy", Op: ">=", Value: 0.35},
			{Field: "mood_aggressive", Op: "<", Value: 0.40},
			{Field: "mood_sad", Op: "<", Value: 0.50},
		},
		SortField: "mood_happy",
	},
	{
		// Light and pleasant — requires happy baseline; exclude aggressive and sad
		Key:   "cooking",
		Label: "Cooking Mix",
		Conditions: []Condition{
			{Field: "mood_happy", Op: ">=", Value: 0.35},
			{Field: "mood_aggressive", Op: "<", Value: 0.45},
			{Field: "mood_sad", Op: "<", Value: 0.45},
		},
		SortField: "mood_happy",
	},
	{
		// Relaxed table atmosphere — requires relaxed baseline; exclude aggressive
		Key:   "dining",
		Label: "Dining Mix",
		Conditions: []Condition{
			{Field: "mood_relaxed", Op: ">=", Value: 0.40},
			{Field: "mood_aggressive", Op: "<", Value: 0.40},
		},
		SortField: "mood_relaxed",
	},
	{
		// Unobtrusive ambient — requires relaxed baseline; exclude aggressive and high-energy party
		Key:   "background",
		Label: "Background Mix",
		Conditions: []Condition{
			{Field: "mood_relaxed", Op: ">=", Value: 0.35},
			{Field: "mood_aggressive", Op: "<", Value: 0.50},
			{Field: "mood_party", Op: "<", Value: 0.55},
		},
		SortField: "mood_relaxed",
	},
}

var defaultGenreExclusions = map[string]string{
	"chill":      "metal, hard rock, punk, hardcore, industrial, grunge, thrash",
	"sleep":      "metal, hard rock, punk, hardcore, industrial, grunge, thrash, dance, techno, trance, house, edm, drum and bass",
	"study":      "metal, punk, hardcore, industrial",
	"dining":     "metal, hard rock, punk, hardcore, industrial",
	"background": "metal, hard rock, punk, hardcore, industrial",
	"road_trip":  "metal, hardcore, industrial",
}

func parseGenreList(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.ToLower(strings.TrimSpace(p)); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func genreExcluded(genre string, excluded []string) bool {
	if genre == "" || len(excluded) == 0 {
		return false
	}
	lower := strings.ToLower(genre)
	for _, ex := range excluded {
		if strings.Contains(lower, ex) {
			return true
		}
	}
	return false
}

// ── Plugin Registration ──────────────────────────────────────────

type moodPlugin struct{}

func main() {
	metadata.Register(&moodPlugin{})
}

// ── Initialization ───────────────────────────────────────────────

//go:wasmexport nd_on_init
func onInit() int32 {
	pdk.Log(pdk.LogInfo, "Mood Playlists plugin initializing...")

	if configBool("auto_analyze", true) {
		schedule := configString("analyze_schedule", "0 2 * * *")
		_, err := host.SchedulerScheduleRecurring(schedule, "analyze", "mood-analyze")
		if err != nil {
			pdk.Log(pdk.LogError, "Failed to schedule analysis: "+err.Error())
		} else {
			pdk.Log(pdk.LogInfo, "Scheduled analysis task: "+schedule)
		}
	}

	refreshSchedule := configString("playlist_refresh_schedule", "0 3 * * 0")
	_, err := host.SchedulerScheduleRecurring(refreshSchedule, "refresh-playlists", "mood-refresh")
	if err != nil {
		pdk.Log(pdk.LogError, "Failed to schedule playlist refresh: "+err.Error())
	} else {
		pdk.Log(pdk.LogInfo, "Scheduled playlist refresh: "+refreshSchedule)
	}

	if reanalyzePct := configInt("reanalyze_percent", 0); reanalyzePct > 0 || configBool("reanalyze_uncertain", true) {
		reanalyzeSchedule := configString("reanalyze_schedule", "0 4 1 * *")
		_, err := host.SchedulerScheduleRecurring(reanalyzeSchedule, "reanalyze", "mood-reanalyze")
		if err != nil {
			pdk.Log(pdk.LogError, "Failed to schedule re-analysis: "+err.Error())
		} else {
			pdk.Log(pdk.LogInfo, "Scheduled re-analysis task: "+reanalyzeSchedule)
		}
	}

	if configBool("run_genre_migration", false) {
		schedule := configString("genre_migration_schedule", "0 1 * * *")
		_, err := host.SchedulerScheduleRecurring(schedule, "migrate-genres", "mood-migrate-genres")
		if err != nil {
			pdk.Log(pdk.LogError, "Failed to schedule genre migration: "+err.Error())
		} else {
			pdk.Log(pdk.LogInfo, "Scheduled genre migration: "+schedule)
		}
	}

	if configBool("enrich_playlists", false) {
		schedule := configString("enrich_schedule", "0 5 * * *")
		_, err := host.SchedulerScheduleRecurring(schedule, "enrich-playlists", "mood-enrich")
		if err != nil {
			pdk.Log(pdk.LogError, "Failed to schedule metadata enrichment: "+err.Error())
		} else {
			pdk.Log(pdk.LogInfo, "Scheduled metadata enrichment task: "+schedule)
		}
	}

	// Clear any stale tasks from previous runs, then ensure the queue exists
	host.TaskClearQueue("mood-analysis")
	concurrency := configInt("max_concurrency", 2)
	if err := host.TaskCreateQueue("mood-analysis", host.QueueConfig{
		Concurrency: int32(concurrency),
		MaxRetries:  3,
	}); err != nil {
		pdk.Log(pdk.LogDebug, "Task queue init: "+err.Error())
	}

	pdk.Log(pdk.LogInfo, "Mood Playlists plugin initialized")
	return 0
}

// ── Scheduled Task Handler ───────────────────────────────────────

//go:wasmexport nd_scheduler_callback
func onSchedule() int32 {
	raw := string(pdk.Input())
	pdk.Log(pdk.LogInfo, "Scheduled task triggered: "+raw)

	// Navidrome passes a JSON object: {"scheduleId":"...","payload":"...","isRecurring":true}
	var envelope struct {
		Payload string `json:"payload"`
	}
	payload := raw
	if err := json.Unmarshal([]byte(raw), &envelope); err == nil && envelope.Payload != "" {
		payload = envelope.Payload
	}

	switch payload {
	case "analyze":
		return runAnalysis()
	case "refresh-playlists":
		return refreshPlaylists()
	case "reanalyze":
		return runScheduledReanalysis()
	case "enrich-playlists":
		return enrichPlaylists()
	case "migrate-genres":
		return migrateGenres()
	default:
		pdk.Log(pdk.LogWarn, "Unknown schedule payload: "+payload)
		return 0
	}
}

// ── Similar Songs (Instant Mix) ──────────────────────────────────

func (p *moodPlugin) GetSimilarSongsByTrack(req metadata.SimilarSongsByTrackRequest) (*metadata.SimilarSongsResponse, error) {
	count := int(req.Count)
	if count <= 0 {
		count = configInt("similar_songs_count", 20)
	}

	sourceKey := "mood:" + req.ID
	sourceData, ok, err := host.KVStoreGet(sourceKey)
	if err != nil || !ok || len(sourceData) == 0 {
		pdk.Log(pdk.LogDebug, "No mood data for track "+req.ID)
		return &metadata.SimilarSongsResponse{}, nil
	}

	var sourceScores MoodScores
	if err := json.Unmarshal(sourceData, &sourceScores); err != nil {
		return nil, fmt.Errorf("failed to parse mood data: %w", err)
	}

	indexData, ok, err := host.KVStoreGet("mood:index")
	if err != nil || !ok || len(indexData) == 0 {
		return &metadata.SimilarSongsResponse{}, nil
	}

	var trackIndex map[string]string
	if err := json.Unmarshal(indexData, &trackIndex); err != nil {
		return &metadata.SimilarSongsResponse{}, nil
	}

	type scoredTrack struct {
		id, name, artist string
		distance         float64
	}

	var candidates []scoredTrack
	for id, info := range trackIndex {
		if id == req.ID {
			continue
		}
		data, ok, err := host.KVStoreGet("mood:" + id)
		if err != nil || !ok || len(data) == 0 {
			continue
		}
		var scores MoodScores
		if err := json.Unmarshal(data, &scores); err != nil {
			continue
		}
		dist := moodDistance(sourceScores, scores)
		parts := strings.SplitN(info, "|", 2)
		name := parts[0]
		artist := ""
		if len(parts) > 1 {
			artist = parts[1]
		}
		candidates = append(candidates, scoredTrack{id: id, name: name, artist: artist, distance: dist})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].distance < candidates[j].distance
	})

	limit := count
	if limit > len(candidates) {
		limit = len(candidates)
	}

	songs := make([]metadata.SongRef, limit)
	for i := 0; i < limit; i++ {
		songs[i] = metadata.SongRef{
			ID:     candidates[i].id,
			Name:   candidates[i].name,
			Artist: candidates[i].artist,
		}
	}

	return &metadata.SimilarSongsResponse{Songs: songs}, nil
}

// ── Analysis Logic ───────────────────────────────────────────────

type pluginTask struct {
	TaskType   string `json:"task_type,omitempty"` // "analyze", "playlist_chunk", or "playlist_finish"
	ID         string `json:"id,omitempty"`
	Title      string `json:"title,omitempty"`
	Artist     string `json:"artist,omitempty"`
	Genre      string `json:"genre,omitempty"`
	Force      bool   `json:"force,omitempty"`      // bypass the skip-if-already-analyzed check
	GenreOnly  bool   `json:"genre_only,omitempty"` // update genre field only, skip audio analysis
	MoodKey    string `json:"mood_key,omitempty"`
	Offset     int    `json:"offset,omitempty"`
}

// isUncertain returns true when no mood score is clearly dominant — the model
// could not confidently classify the track and a re-analysis may improve it.
func isUncertain(scores *MoodScores) bool {
	max := math.Max(scores.MoodHappy, math.Max(scores.MoodSad,
		math.Max(scores.MoodRelaxed, math.Max(scores.MoodAggressive, scores.MoodParty))))
	return max < 0.45 || scores.BPM == 0
}

// markUncertain records a track ID for future re-analysis.
// Stored as a JSON map in KV under "mood:reanalyze". Capped at 1000 entries.
// Concurrent writes from multiple workers are accepted — occasional lost IDs
// are harmless; they will be caught in a future analysis run.
func markUncertain(id string) {
	data, ok, _ := host.KVStoreGet("mood:reanalyze")
	var ids map[string]bool
	if ok && len(data) > 0 {
		json.Unmarshal(data, &ids)
	}
	if ids == nil {
		ids = make(map[string]bool)
	}
	if len(ids) >= 1000 {
		return
	}
	if ids[id] {
		return
	}
	ids[id] = true
	encoded, _ := json.Marshal(ids)
	host.KVStoreSet("mood:reanalyze", encoded)
}

func runAnalysis() int32 {
	// Queue as many batches as possible within the time budget.
	// Progress (offset) is persisted in KV store so each run continues where
	// the last one left off. Resets to 0 when the end of the library is reached.
	const batchSize = 500
	const deadline = 20 * time.Second

	offsetData, _, _ := host.KVStoreGet("analysis:offset")
	offset := 0
	if len(offsetData) > 0 {
		offset, _ = strconv.Atoi(string(offsetData))
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Queuing tracks for mood analysis (offset %d)...", offset))

	start := time.Now()
	totalQueued := 0

	for {
		uri := fmt.Sprintf("search3?query=&songCount=%d&songOffset=%d&albumCount=0&artistCount=0", batchSize, offset)
		respStr, err := subsonicCall(uri)
		if err != nil {
			pdk.Log(pdk.LogError, "Subsonic API search failed: "+err.Error())
			break
		}

		var result struct {
			SubsonicResponse struct {
				SearchResult3 struct {
					Song []struct {
						ID     string `json:"id"`
						Title  string `json:"title"`
						Artist string `json:"artist"`
						Path   string `json:"path"`
						Genre  string `json:"genre"`
					} `json:"song"`
				} `json:"searchResult3"`
			} `json:"subsonic-response"`
		}

		if err := json.Unmarshal([]byte(respStr), &result); err != nil {
			pdk.Log(pdk.LogError, "Failed to parse search results: "+err.Error())
			break
		}

		songs := result.SubsonicResponse.SearchResult3.Song
		endOfLibrary := len(songs) == 0 || len(songs) < batchSize

		for _, song := range songs {
			// Skip tracks already analyzed — avoids unnecessary work when library is fully analyzed
			if existing, ok, _ := host.KVStoreGet("mood:" + song.ID); ok && len(existing) > 0 {
				continue
			}
			taskData, _ := json.Marshal(pluginTask{
				ID:     song.ID,
				Title:  song.Title,
				Artist: song.Artist,
				Genre:  song.Genre,
			})
			if _, err := host.TaskEnqueue("mood-analysis", taskData); err != nil {
				pdk.Log(pdk.LogWarn, "Failed to queue "+song.Title+": "+err.Error())
				continue
			}
			totalQueued++
		}

		if endOfLibrary {
			// Reset offset for next full pass
			offset = 0
			host.KVStoreSet("analysis:offset", []byte("0"))
			pdk.Log(pdk.LogInfo, fmt.Sprintf("Reached end of library, queued %d new tracks — offset reset to 0", totalQueued))

			// Re-analysis phase — only runs when no new tracks were found (library fully analyzed)
			if totalQueued == 0 {
				queueReanalysis(start, deadline)
			}
			return 0
		}

		offset += len(songs)
		host.KVStoreSet("analysis:offset", []byte(strconv.Itoa(offset)))

		// Stop fetching if we are approaching the deadline
		if time.Since(start) >= deadline {
			break
		}
	}

	host.KVStoreSet("analysis:offset", []byte(strconv.Itoa(offset)))
	pdk.Log(pdk.LogInfo, fmt.Sprintf("Queued %d tracks (next offset: %d)", totalQueued, offset))
	return 0
}

// migrateGenres is the scheduler entry point — it simply kicks off the first
// chunk task and returns immediately, staying well within the 30-second limit.
// The chunk tasks run in the task queue (no timeout) and chain themselves until
// the entire library has been processed.
func migrateGenres() int32 {
	pdk.Log(pdk.LogInfo, "Starting genre migration — queuing first chunk...")
	taskData, _ := json.Marshal(pluginTask{TaskType: "genre_migrate_chunk", Offset: 0})
	if _, err := host.TaskEnqueue("mood-analysis", taskData); err != nil {
		pdk.Log(pdk.LogError, "Failed to queue genre migration: "+err.Error())
		return 1
	}
	return 0
}

// processMigrateGenreChunk handles one batch of the genre migration.
// For each analyzed track missing a genre it queues a GenreOnly update task,
// then chains to the next chunk until the library is exhausted.
func processMigrateGenreChunk(offset int) int32 {
	const batchSize = 500

	uri := fmt.Sprintf("search3?query=&songCount=%d&songOffset=%d&albumCount=0&artistCount=0", batchSize, offset)
	respStr, err := subsonicCall(uri)
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Genre migration chunk %d: Subsonic search failed: %s", offset, err.Error()))
		return 1
	}

	var result struct {
		SubsonicResponse struct {
			SearchResult3 struct {
				Song []struct {
					ID     string `json:"id"`
					Title  string `json:"title"`
					Artist string `json:"artist"`
					Genre  string `json:"genre"`
				} `json:"song"`
			} `json:"searchResult3"`
		} `json:"subsonic-response"`
	}
	if err := json.Unmarshal([]byte(respStr), &result); err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Genre migration chunk %d: parse error: %s", offset, err.Error()))
		return 1
	}

	// Update genre directly in KV — no need to queue individual tasks
	songs := result.SubsonicResponse.SearchResult3.Song
	updated := 0
	for _, song := range songs {
		existing, ok, _ := host.KVStoreGet("mood:" + song.ID)
		if !ok || len(existing) == 0 {
			continue
		}
		var scores MoodScores
		if err := json.Unmarshal(existing, &scores); err != nil || scores.Genre != "" {
			continue
		}
		scores.Genre = song.Genre
		data, _ := json.Marshal(scores)
		if err := host.KVStoreSet("mood:"+song.ID, data); err == nil {
			updated++
		}
	}

	if len(songs) < batchSize {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("Genre migration complete — final chunk %d, updated %d tracks", offset, updated))
		return 0
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Genre migration chunk %d-%d: updated %d tracks, continuing...", offset, offset+batchSize, updated))
	nextTask, _ := json.Marshal(pluginTask{TaskType: "genre_migrate_chunk", Offset: offset + batchSize})
	if _, err := host.TaskEnqueue("mood-analysis", nextTask); err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Genre migration: failed to queue next chunk at offset %d: %s", offset+batchSize, err.Error()))
		return 1
	}
	return 0
}

// runScheduledReanalysis is the handler for the dedicated re-analysis schedule.
// Unlike the inline re-analysis in runAnalysis() (which only fires when there
// are no new tracks), this runs on its own cron and always processes uncertain
// tracks and the configured random percentage — ensuring scores improve even
// in libraries that receive frequent additions.
func runScheduledReanalysis() int32 {
	pdk.Log(pdk.LogInfo, "Running scheduled re-analysis...")
	const deadline = 20 * time.Second
	start := time.Now()
	queueReanalysis(start, deadline)
	return 0
}

// queueReanalysis handles two types of re-analysis after a full library pass:
//  1. Uncertain tracks — previously analyzed but with low-confidence scores
//  2. Random percentage — a configurable fraction of all tracks, for gradual score refresh
func queueReanalysis(start time.Time, deadline time.Duration) {
	indexData, ok, _ := host.KVStoreGet("mood:index")
	var index map[string]string // id → "title|artist"
	if ok && len(indexData) > 0 {
		json.Unmarshal(indexData, &index)
	}

	// 1. Re-analyze uncertain tracks
	if configBool("reanalyze_uncertain", true) {
		data, ok, _ := host.KVStoreGet("mood:reanalyze")
		if ok && len(data) > 0 {
			var ids map[string]bool
			if err := json.Unmarshal(data, &ids); err == nil && len(ids) > 0 {
				requeued := 0
				for id := range ids {
					if time.Since(start) >= deadline {
						break
					}

					// Preserve existing metadata if available to prevent the "empty name" bug
					title, artist := "", ""
					if index != nil {
						if info, found := index[id]; found {
							parts := strings.SplitN(info, "|", 2)
							title = parts[0]
							if len(parts) > 1 {
								artist = parts[1]
							}
						}
					}

					taskData, _ := json.Marshal(pluginTask{ID: id, Title: title, Artist: artist, Force: true})
					if _, err := host.TaskEnqueue("mood-analysis", taskData); err == nil {
						requeued++
					}
				}
				// Clear queue — successfully re-queued tracks will update their own status
				host.KVStoreSet("mood:reanalyze", []byte("{}"))
				pdk.Log(pdk.LogInfo, fmt.Sprintf("Queued %d uncertain tracks for re-analysis", requeued))
			}
		}
	}

	// 2. Random re-analysis percentage
	reanalyzePct := configInt("reanalyze_percent", 0)
	if reanalyzePct <= 0 || time.Since(start) >= deadline || index == nil {
		return
	}

	// Collect and shuffle all IDs
	allIDs := make([]string, 0, len(index))
	for id := range index {
		allIDs = append(allIDs, id)
	}
	rand.Shuffle(len(allIDs), func(i, j int) { allIDs[i], allIDs[j] = allIDs[j], allIDs[i] })

	sampleSize := len(allIDs) * reanalyzePct / 100
	requeued := 0
	for _, id := range allIDs[:sampleSize] {
		if time.Since(start) >= deadline {
			break
		}
		info := index[id]
		parts := strings.SplitN(info, "|", 2)
		title, artist := parts[0], ""
		if len(parts) > 1 {
			artist = parts[1]
		}
		taskData, _ := json.Marshal(pluginTask{ID: id, Title: title, Artist: artist, Force: true})
		if _, err := host.TaskEnqueue("mood-analysis", taskData); err == nil {
			requeued++
		}
	}
	pdk.Log(pdk.LogInfo, fmt.Sprintf("Queued %d tracks for random re-analysis (%d%% of library)", requeued, reanalyzePct))
}

// ── Task Executor ─────────────────────────────────────────────────

//go:wasmexport nd_task_execute
func onTaskExecute() int32 {
	raw := pdk.Input()

	// Navidrome wraps the payload in a JSON envelope: {"taskId":"...","payload":"base64...","attempt":1}
	var envelope struct {
		Payload []byte `json:"payload"`
	}
	taskData := raw
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Payload) > 0 {
		taskData = envelope.Payload
	}

	var task pluginTask
	if err := json.Unmarshal(taskData, &task); err != nil {
		pdk.Log(pdk.LogError, "Failed to parse task payload: "+err.Error())
		return 1
	}

	if task.TaskType == "playlist_chunk" {
		return handlePlaylistChunk(task.Offset)
	}
	if task.TaskType == "playlist_finish" {
		return finishPlaylistGeneration()
	}
	if task.TaskType == "genre_migrate_chunk" {
		return processMigrateGenreChunk(task.Offset)
	}

	// Skip if already analyzed, unless this is a forced re-analysis task
	key := "mood:" + task.ID
	existing, ok, _ := host.KVStoreGet(key)
	if ok && len(existing) > 0 && !task.Force && !task.GenreOnly {
		return 0
	}

	// Genre-only migration: patch the stored scores with genre, skip audio analysis
	if task.GenreOnly && task.Genre != "" {
		if ok && len(existing) > 0 {
			var scores MoodScores
			if err := json.Unmarshal(existing, &scores); err == nil {
				scores.Genre = task.Genre
				data, _ := json.Marshal(scores)
				host.KVStoreSet(key, data)
			}
		}
		return 0
	}

	analyzerURL := configString("analyzer_url", "http://mood-analyzer:8000")
	ndURL := configString("navidrome_url", "http://navidrome:4533")
	user := configString("navidrome_user", "")
	pass := configString("navidrome_password", "")
	streamURL := subsonicStreamURL(ndURL, user, pass, task.ID)

	scores, err := callAnalyzerURL(analyzerURL, streamURL)
	if err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("Analysis failed for %s: %s", task.Title, err.Error()))
		return 1
	}
	scores.Genre = task.Genre

	data, _ := json.Marshal(scores)
	if err := host.KVStoreSet(key, data); err != nil {
		pdk.Log(pdk.LogWarn, "Failed to store mood data for "+task.Title)
		return 1
	}

	updateIndex(task.ID, task.Title, task.Artist)

	// Flag uncertain results for re-analysis on the next cycle
	if isUncertain(scores) {
		markUncertain(task.ID)
	}

	pdk.Log(pdk.LogInfo, "Analyzed: "+task.Title)
	return 0
}

func callAnalyzerURL(baseURL, streamURL string) (*MoodScores, error) {
	reqBody, _ := json.Marshal(map[string]string{"url": streamURL})

	resp, err := host.HTTPSend(host.HTTPRequest{
		URL:       baseURL + "/api/analysis/url",
		Method:    "POST",
		Body:      reqBody,
		Headers:   map[string]string{"Content-Type": "application/json"},
		TimeoutMs: 300000, // 5 min — download + TF analysis
	})
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("analyzer returned status %d", resp.StatusCode)
	}

	var analysis AnalysisResponse
	if err := json.Unmarshal(resp.Body, &analysis); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &MoodScores{
		MoodHappy:      analysis.MoodHappy,
		MoodSad:        analysis.MoodSad,
		MoodRelaxed:    analysis.MoodRelaxed,
		MoodAggressive: analysis.MoodAggressive,
		MoodParty:      analysis.MoodParty,
		Danceability:   analysis.Danceability,
		BPM:            analysis.BPM,
		Energy:         analysis.Energy,
		Arousal:        analysis.Arousal,
		Valence:        analysis.Valence,
	}, nil
}

func callAnalyzer(baseURL, filePath string) (*MoodScores, error) {
	reqBody, _ := json.Marshal(map[string]string{"file_path": filePath})

	resp, err := host.HTTPSend(host.HTTPRequest{
		URL:       baseURL + "/api/analysis/file",
		Method:    "POST",
		Body:      reqBody,
		Headers:   map[string]string{"Content-Type": "application/json"},
		TimeoutMs: 120000,
	})
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("analyzer returned status %d", resp.StatusCode)
	}

	var analysis AnalysisResponse
	if err := json.Unmarshal(resp.Body, &analysis); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &MoodScores{
		MoodHappy:      analysis.MoodHappy,
		MoodSad:        analysis.MoodSad,
		MoodRelaxed:    analysis.MoodRelaxed,
		MoodAggressive: analysis.MoodAggressive,
		MoodParty:      analysis.MoodParty,
		Danceability:   analysis.Danceability,
		BPM:            analysis.BPM,
		Energy:         analysis.Energy,
		Arousal:        analysis.Arousal,
		Valence:        analysis.Valence,
	}, nil
}

// ── Playlist Generation ──────────────────────────────────────────

// selectTracks picks up to limit tracks from a sorted candidate list,
// allowing at most maxPerArtist tracks from any single artist.
// An empty or blank artist name is treated as "unknown" and counted together.
// poolMultiplier controls weekly variation: the top limit*poolMultiplier tracks
// are shuffled before selection so each run draws from the same quality pool
// but produces a different playlist. Set to 1 to disable shuffling.
func selectTracks(sorted []trackWithScores, limit, maxPerArtist, poolMultiplier int) []string {
	// Build candidate pool from top-scoring tracks and shuffle for variation
	pool := sorted
	if poolMultiplier > 1 && len(sorted) > limit {
		poolSize := limit * poolMultiplier
		if poolSize > len(sorted) {
			poolSize = len(sorted)
		}
		candidates := make([]trackWithScores, poolSize)
		copy(candidates, sorted[:poolSize])
		rand.Shuffle(len(candidates), func(i, j int) {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		})
		pool = candidates
	}

	artistCount := make(map[string]int)
	seen := make(map[string]bool) // dedup by normalised title+artist
	var ids []string
	for _, t := range pool {
		if len(ids) >= limit {
			break
		}
		artist := strings.ToLower(strings.TrimSpace(t.Artist))
		if artist == "" {
			artist = "__unknown__"
		}
		title := strings.ToLower(strings.TrimSpace(t.Name))
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

func refreshPlaylists() int32 {
	pdk.Log(pdk.LogInfo, "Starting chunked playlist generation...")
	
	// Clear any temporary candidate data from previous runs
	for _, m := range moods {
		host.KVStoreSet("temp:playlist:"+m.Key, []byte("[]"))
	}
	for _, m := range compositeMoods {
		host.KVStoreSet("temp:playlist:"+m.Key, []byte("[]"))
	}

	indexData, ok, _ := host.KVStoreGet("mood:index")
	if !ok || len(indexData) == 0 {
		pdk.Log(pdk.LogWarn, "No analyzed tracks found — run analysis first")
		return 0
	}

	var trackIndex map[string]string
	if err := json.Unmarshal(indexData, &trackIndex); err != nil {
		pdk.Log(pdk.LogError, "Failed to parse track index: "+err.Error())
		return 1
	}

	// PERFORMANCE OPTIMIZATION (v0.5.9): 
	// Collect and sort IDs ONCE at the start. Sorting 86k strings is expensive.
	// We save this sorted list to a temporary key to avoid repeating this in every chunk.
	allIDs := make([]string, 0, len(trackIndex))
	for id := range trackIndex {
		allIDs = append(allIDs, id)
	}
	sort.Strings(allIDs)
	
	idListJSON, _ := json.Marshal(allIDs)
	host.KVStoreSet("temp:playlist_ids", idListJSON)

	// Kick off the first chunk
	taskData, _ := json.Marshal(pluginTask{TaskType: "playlist_chunk", Offset: 0})
	if _, err := host.TaskEnqueue("mood-analysis", taskData); err != nil {
		pdk.Log(pdk.LogError, "Failed to queue first playlist chunk: "+err.Error())
		return 1
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Playlist generation started (Library: %d tracks)", len(allIDs)))
	return 0
}

func handlePlaylistChunk(offset int) int32 {
	// PERFORMANCE TWEAK (v0.7.0):
	// Reduced chunk size to 1000 for maximum safety on low-power hardware.
	const chunkSize = 1000 
	const maxCandidates = 500 // Hold up to 500 best candidates for each mood in temporary storage

	// Load the cached sorted IDs
	idListData, ok, _ := host.KVStoreGet("temp:playlist_ids")
	if !ok || len(idListData) == 0 {
		return 0
	}
	var allIDs []string
	json.Unmarshal(idListData, &allIDs)

	if offset >= len(allIDs) {
		// We've processed the entire library, trigger the finisher
		taskData, _ := json.Marshal(pluginTask{TaskType: "playlist_finish"})
		host.TaskEnqueue("mood-analysis", taskData)
		return 0
	}

	// PERFORMANCE OPTIMIZATION (v0.7.0): 
	// Removed mood:index parsing from this phase. We only need IDs and Scores here.
	// Title/Artist lookups are deferred to the final finisher task.

	end := offset + chunkSize
	if end > len(allIDs) {
		end = len(allIDs)
	}

	// 1. Process this chunk of tracks for ALL moods
	chunkTracks := make([]trackWithScores, 0, chunkSize)
	for _, id := range allIDs[offset:end] {
		data, ok, _ := host.KVStoreGet("mood:" + id)
		if !ok || len(data) == 0 {
			continue
		}
		var scores MoodScores
		if err := json.Unmarshal(data, &scores); err != nil {
			continue
		}
		
		// Note: Name and Artist are left blank here to keep temporary KV storage small.
		chunkTracks = append(chunkTracks, trackWithScores{ID: id, Scores: scores})
	}

	// 2. Evaluate and merge candidates for each mood
	processMoodCandidates := func(moodKey string, label string, isComposite bool, conditions []Condition, scoreField string, threshold float64, sortField string) {
		key := "temp:playlist:" + moodKey
		data, _, _ := host.KVStoreGet(key)
		var existing []trackWithScores
		json.Unmarshal(data, &existing)

		// Filter chunk for this mood
		excludedGenres := parseGenreList(configString(moodKey+"_excluded_genres", defaultGenreExclusions[moodKey]))
		var matches []trackWithScores
		for _, t := range chunkTracks {
			if genreExcluded(t.Scores.Genre, excludedGenres) {
				continue
			}
			if isComposite {
				if matchesAllConditions(t.Scores, conditions) {
					matches = append(matches, t)
				}
			} else {
				// Simple mood logic
				if getScore(t.Scores, scoreField) >= threshold {
					excluded := false
					for _, ex := range conditions { // For simple moods, conditions = Exclude list
						val := getScore(t.Scores, ex.Field)
						if (ex.Op == ">=" && val >= ex.Value) || (ex.Op == "<" && val < ex.Value) {
							excluded = true
							break
						}
					}
					if !excluded {
						matches = append(matches, t)
					}
				}
			}
		}

		// Merge and sort
		all := append(existing, matches...)
		finalSortField := scoreField
		if isComposite {
			finalSortField = sortField
		}
		sort.Slice(all, func(i, j int) bool {
			return getScore(all[i].Scores, finalSortField) > getScore(all[j].Scores, finalSortField)
		})

		// Cap size to keep KV value small
		if len(all) > maxCandidates {
			all = all[:maxCandidates]
		}
		encoded, _ := json.Marshal(all)
		host.KVStoreSet(key, encoded)
	}

	// Simple moods
	for _, m := range moods {
		threshold := configFloat(m.ThresholdKey, m.DefaultThresh)
		processMoodCandidates(m.Key, m.Label, false, m.Exclude, m.ScoreField, threshold, "")
	}
	// Composite moods
	for _, m := range compositeMoods {
		processMoodCandidates(m.Key, m.Label, true, m.Conditions, "", 0, m.SortField)
	}

	// 3. Queue next chunk or finisher
	nextOffset := offset + chunkSize
	var nextTask pluginTask
	if nextOffset >= len(allIDs) {
		nextTask = pluginTask{TaskType: "playlist_finish"}
		pdk.Log(pdk.LogInfo, fmt.Sprintf("Chunk processed (%d-%d) — library complete, queuing finisher", offset, end))
	} else {
		nextTask = pluginTask{TaskType: "playlist_chunk", Offset: nextOffset}
		pdk.Log(pdk.LogInfo, fmt.Sprintf("Chunk processed (%d-%d) — queuing next chunk", offset, end))
	}
	taskData, _ := json.Marshal(nextTask)
	host.TaskEnqueue("mood-analysis", taskData)
	return 0
}

func finishPlaylistGeneration() int32 {
	pdk.Log(pdk.LogInfo, "Finishing playlist generation...")
	trackCount := configInt("playlist_track_count", 30)
	poolMultiplier := configInt("playlist_variation_pool", 3)
	maxPerArtist := configInt("max_tracks_per_artist", 3)
	existingIDs := getExistingPlaylistIDs()

	// PERFORMANCE OPTIMIZATION (v0.7.0): 
	// Load the index once at the end to hydrate metadata for the selected tracks.
	indexData, _, _ := host.KVStoreGet("mood:index")
	var trackIndex map[string]string
	json.Unmarshal(indexData, &trackIndex)

	process := func(moodKey string, label string) {
		key := "temp:playlist:" + moodKey
		data, ok, _ := host.KVStoreGet(key)
		if !ok || len(data) == 0 {
			return
		}
		var candidates []trackWithScores
		json.Unmarshal(data, &candidates)

		// HYDRATION PHASE:
		// Fill in the missing Name/Artist for candidates before selection.
		if trackIndex != nil {
			for i := range candidates {
				if info, found := trackIndex[candidates[i].ID]; found {
					parts := strings.SplitN(info, "|", 2)
					candidates[i].Name = parts[0]
					if len(parts) > 1 {
						candidates[i].Artist = parts[1]
					}
				}
			}
		}

		songIDs := selectTracks(candidates, trackCount, maxPerArtist, poolMultiplier)
		if len(songIDs) > 0 {
			upsertPlaylist(label, songIDs, existingIDs)
		} else {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("No qualifying tracks for '%s' — playlist not updated", label))
		}
		// Cleanup
		host.KVStoreSet(key, []byte("[]"))
	}

	for _, m := range moods {
		process(m.Key, m.Label)
	}
	for _, m := range compositeMoods {
		process(m.Key, m.Label)
	}

	// Cleanup the ID list
	host.KVStoreSet("temp:playlist_ids", []byte("[]"))

	pdk.Log(pdk.LogInfo, "Mood playlists successfully refreshed!")
	return 0
}

func generateSinglePlaylist(moodKey string) int32 {
	// This function is now deprecated in favor of chunked generation but kept for safety/compatibility.
	// It just triggers a full refresh.
	return refreshPlaylists()
}

func matchesAllConditions(scores MoodScores, conditions []Condition) bool {
	for _, c := range conditions {
		val := getScore(scores, c.Field)
		switch c.Op {
		case ">=":
			if val < c.Value {
				return false
			}
		case "<":
			if val >= c.Value {
				return false
			}
		}
	}
	return true
}

// getExistingPlaylistIDs returns a map of playlist name → id for all playlists
// visible to the configured user. Used by refreshPlaylists to update rather than
// duplicate when a mood playlist already exists.
func getExistingPlaylistIDs() map[string]string {
	result := map[string]string{}
	body, err := subsonicCall("getPlaylists.view?")
	if err != nil {
		pdk.Log(pdk.LogWarn, "getPlaylists failed: "+err.Error())
		return result
	}
	// Parse just what we need: {"subsonic-response":{"playlists":{"playlist":[{"id":"...","name":"..."}]}}}
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

// upsertPlaylist creates a new playlist or replaces the tracks in an existing one.
// It conditionally appends a "last generated" date to the name, and matches existing playlists
// by checking if their name starts with the baseLabel.
func upsertPlaylist(baseLabel string, songIDs []string, existingIDs map[string]string) {
	now := time.Now().Format("02 Jan, 15:04")
	
	fullName := baseLabel
	if configBool("show_dates_in_title", true) {
		fullName = fmt.Sprintf("%s (%s)", baseLabel, now)
	}

	commentStr := "Last generated: " + now

	var params string
	var plID string
	isUpdate := false

	// Find the existing playlist by looking for any playlist that starts with the base label
	for name, id := range existingIDs {
		if strings.HasPrefix(name, baseLabel) {
			plID = id
			break
		}
	}

	if plID != "" {
		// Update existing playlist — replace all tracks and update name
		params = "playlistId=" + url.QueryEscape(plID) + "&name=" + url.QueryEscape(fullName)
		isUpdate = true
	} else {
		// Create brand-new playlist
		params = "name=" + url.QueryEscape(fullName)
	}
	for _, id := range songIDs {
		params += "&songId=" + url.QueryEscape(id)
	}

	_, err := subsonicCall("createPlaylist?" + params)
	if err != nil {
		pdk.Log(pdk.LogError, "Failed to upsert playlist '"+fullName+"': "+err.Error())
		return
	}

	// For new playlists, fetch the newly created ID so we can set the comment.
	if !isUpdate {
		existingIDs = getExistingPlaylistIDs()
		for name, id := range existingIDs {
			if strings.HasPrefix(name, baseLabel) {
				plID = id
				break
			}
		}
	}

	// Save the timestamp to the comment field as well (and force rename)
	if plID != "" {
		updateParams := "playlistId=" + url.QueryEscape(plID) + "&name=" + url.QueryEscape(fullName) + "&comment=" + url.QueryEscape(commentStr)
		subsonicCall("updatePlaylist?" + updateParams)
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Updated playlist '%s' (%d tracks)", fullName, len(songIDs)))
}

// ── Subsonic auth helpers ─────────────────────────────────────────

// subsonicTokenAuth returns token-auth query params (t= and s=) for the Subsonic API.
// This avoids embedding the raw password in URLs — t = MD5(password + salt).
func subsonicTokenAuth(pass string) (token, salt string) {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 10)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	salt = string(b)
	h := md5.Sum([]byte(pass + salt))
	token = hex.EncodeToString(h[:])
	return
}

// subsonicStreamURL builds a Subsonic stream URL using token auth instead of plain password.
func subsonicStreamURL(ndURL, user, pass, trackID string) string {
	t, s := subsonicTokenAuth(pass)
	return fmt.Sprintf("%s/rest/stream?id=%s&u=%s&t=%s&s=%s&v=1.16.1&c=mood-plugin",
		strings.TrimRight(ndURL, "/"),
		url.QueryEscape(trackID),
		url.QueryEscape(user),
		t, s)
}

// ── Subsonic HTTP helper (for scheduler context where no user is injected) ───

func subsonicCall(uri string) (string, error) {
	ndURL := configString("navidrome_url", "http://navidrome:4533")
	user := configString("navidrome_user", "")
	pass := configString("navidrome_password", "")

	if user == "" {
		// No credentials configured — fall back to host call (works in user context)
		return host.SubsonicAPICall(uri)
	}

	sep := "?"
	if strings.Contains(uri, "?") {
		sep = "&"
	}
	fullURL := fmt.Sprintf("%s/rest/%s%su=%s&p=%s&v=1.16.1&c=mood-plugin&f=json",
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

// ── Helpers ──────────────────────────────────────────────────────

func updateIndex(id, title, artist string) {
	indexData, ok, _ := host.KVStoreGet("mood:index")
	var index map[string]string
	if ok && len(indexData) > 0 {
		json.Unmarshal(indexData, &index)
	}
	if index == nil {
		index = make(map[string]string)
	}

	// Safeguard: Do not overwrite an existing name with a blank string.
	// This heals the "missing song names" bug reported in v0.5.8.
	if title == "" {
		if _, found := index[id]; found {
			return // Keep what we have
		}
	}

	index[id] = title + "|" + artist
	data, _ := json.Marshal(index)
	host.KVStoreSet("mood:index", data)
}

func getScore(scores MoodScores, field string) float64 {
	switch field {
	case "mood_happy":
		return scores.MoodHappy
	case "mood_sad":
		return scores.MoodSad
	case "mood_relaxed":
		return scores.MoodRelaxed
	case "mood_aggressive":
		return scores.MoodAggressive
	case "mood_party":
		return scores.MoodParty
	case "danceability":
		return scores.Danceability
	case "bpm":
		return scores.BPM
	case "energy":
		return scores.Energy
	case "arousal":
		return scores.Arousal
	case "valence":
		return scores.Valence
	default:
		return 0
	}
}

func moodDistance(a, b MoodScores) float64 {
	return math.Sqrt(
		sq(a.MoodHappy-b.MoodHappy) +
			sq(a.MoodSad-b.MoodSad) +
			sq(a.MoodRelaxed-b.MoodRelaxed) +
			sq(a.MoodAggressive-b.MoodAggressive) +
			sq(a.MoodParty-b.MoodParty) +
			sq(a.Danceability-b.Danceability) +
			sq(a.Arousal-b.Arousal) +
			sq(a.Valence-b.Valence) +
			sq((a.BPM-b.BPM)/200),
	)
}

func sq(x float64) float64 { return x * x }

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

func configFloat(key string, defaultVal float64) float64 {
	val, ok := host.ConfigGet(key)
	if !ok || val == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return defaultVal
	}
	return f
}

func configBool(key string, defaultVal bool) bool {
	val, ok := host.ConfigGet(key)
	if !ok || val == "" {
		return defaultVal
	}
	return val == "true" || val == "1" || val == "yes"
}

// enrichPlaylists iterates through all playlists in the library and appends
// a "[Created: YYYY-MM-DD]" tag to their comments if not already present.
func enrichPlaylists() int32 {
	pdk.Log(pdk.LogInfo, "Running metadata enrichment for all playlists...")
	body, err := subsonicCall("getPlaylists.view?")
	if err != nil {
		pdk.Log(pdk.LogError, "getPlaylists failed: "+err.Error())
		return 1
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
		pdk.Log(pdk.LogError, "Failed to parse playlists: "+err.Error())
		return 1
	}

	currentUser := configString("navidrome_user", "")
	showInTitle := configBool("show_dates_in_title", true)
	enrichedCount := 0
	skipCount := 0

	for _, pl := range resp.SubsonicResponse.Playlists.Playlist {
		// Only enrich playlists owned by the plugin user to avoid "User not authorized" errors.
		// If navidrome_user is not set, we are likely running in a context where ownership is implicit or managed by the host.
		if currentUser != "" && pl.Owner != "" && !strings.EqualFold(pl.Owner, currentUser) {
			skipCount++
			continue
		}

		// Skip if it's a mood playlist (they manage their own dates)
		if strings.Contains(pl.Comment, "Last generated:") || strings.Contains(pl.Name, " Mix (") || strings.HasSuffix(pl.Name, " Mix") {
			continue
		}

		hasTitleTag := strings.Contains(pl.Name, "(Created:")
		hasCommentTag := strings.Contains(pl.Comment, "Created:")

		needsTitleUpdate := (showInTitle && !hasTitleTag) || (!showInTitle && hasTitleTag)
		needsCommentUpdate := !hasCommentTag

		if !needsTitleUpdate && !needsCommentUpdate {
			continue
		}

		// Parse ISO 8601 (e.g. 2021-02-23T04:35:38+00:00) and format to (DD MMM, HH:MM)
		createdDate := pl.Created
		if t, err := time.Parse(time.RFC3339, pl.Created); err == nil {
			createdDate = t.Format("02 Jan, 15:04")
		} else if len(pl.Created) >= 10 {
			// Fallback if parsing fails but we have at least a date string
			createdDate = pl.Created[:10]
		}

		newName := pl.Name
		if showInTitle && !hasTitleTag {
			newName = fmt.Sprintf("%s (Created: %s)", pl.Name, createdDate)
		} else if !showInTitle && hasTitleTag {
			// Strip the tag from the name
			idx := strings.LastIndex(pl.Name, " (Created:")
			if idx != -1 {
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

		// Update the playlist metadata using Subsonic API
		params := "playlistId=" + url.QueryEscape(pl.ID) + "&name=" + url.QueryEscape(newName) + "&comment=" + url.QueryEscape(newComment)
		if _, err := subsonicCall("updatePlaylist?" + params); err != nil {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("Failed to update playlist '%s': %s", pl.Name, err.Error()))
			continue
		}
		enrichedCount++
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Metadata enrichment complete. Updated %d playlists.", enrichedCount))
	return 0
}

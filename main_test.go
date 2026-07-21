package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// resetMocks clears expectations/calls recorded on the shared PDK mock
// instances so each test starts from a clean slate.
func resetMocks() {
	host.ConfigMock.ExpectedCalls, host.ConfigMock.Calls = nil, nil
	host.SchedulerMock.ExpectedCalls, host.SchedulerMock.Calls = nil, nil
	host.SubsonicAPIMock.ExpectedCalls, host.SubsonicAPIMock.Calls = nil, nil
	host.TaskMock.ExpectedCalls, host.TaskMock.Calls = nil, nil
	host.HTTPMock.ExpectedCalls, host.HTTPMock.Calls = nil, nil
	pdk.PDKMock.ExpectedCalls, pdk.PDKMock.Calls = nil, nil
	pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Return().Maybe()

	// subsonicCall always reads all three connection settings up front, even
	// on the no-credentials fallback path - default them to "unset" so
	// individual tests only need to override what they actually care about.
	host.ConfigMock.On("Get", "navidrome_url").Return("", false).Maybe()
	host.ConfigMock.On("Get", "navidrome_user").Return("", false).Maybe()
	host.ConfigMock.On("Get", "navidrome_password").Return("", false).Maybe()

	// startRebuild always reads both per-category allowlists up front -
	// default them to "unset" (= no narrowing) so tests only need to
	// override the one(s) they actually care about.
	host.ConfigMock.On("Get", "genre_allowlist").Return("", false).Maybe()
	host.ConfigMock.On("Get", "mood_allowlist").Return("", false).Maybe()
}

// ── Pure logic ───────────────────────────────────────────────────

func TestTitleCase(t *testing.T) {
	require.Equal(t, "New Age", titleCase("new age"))
	require.Equal(t, "Rock", titleCase("rock"))
	require.Equal(t, "Hip Hop", titleCase("hip hop"))
}

func TestTagLabel(t *testing.T) {
	require.Equal(t, "AI Chill Mix", tagLabel("mood:chill"))
	require.Equal(t, "AI New Age", tagLabel("genre:new age"))
	require.Equal(t, "AI Untagged", tagLabel("untagged"))
}

func TestParseList(t *testing.T) {
	require.Equal(t, []string{"genre", "mood"}, parseList("genre, mood"))
	require.Equal(t, []string{"genre"}, parseList("  genre  "))
	require.Empty(t, parseList(""))
}

func TestConfigStringArray(t *testing.T) {
	resetMocks()
	host.ConfigMock.ExpectedCalls, host.ConfigMock.Calls = nil, nil // avoid the blanket defaults below
	host.ConfigMock.On("Get", "genre_allowlist").Return(`["rock","pop"]`, true).Once()
	host.ConfigMock.On("Get", "unset_field").Return("", false).Once()
	host.ConfigMock.On("Get", "malformed_field").Return("not json", true).Once()

	require.Equal(t, []string{"rock", "pop"}, configStringArray("genre_allowlist"))
	require.Nil(t, configStringArray("unset_field"))
	require.Nil(t, configStringArray("malformed_field"))
}

func TestSelectTracks_AppliesPerArtistCap(t *testing.T) {
	var candidates []taggedTrack
	for i := 0; i < 5; i++ {
		candidates = append(candidates, taggedTrack{ID: "a" + string(rune('0'+i)), Title: "T", Artist: "ArtistA"})
	}
	candidates = append(candidates, taggedTrack{ID: "b1", Title: "T2", Artist: "ArtistB"})

	ids := selectTracks(candidates, 10, 2)

	artistACounts := 0
	for _, id := range ids {
		if id[0] == 'a' {
			artistACounts++
		}
	}
	require.LessOrEqual(t, artistACounts, 2)
	require.Contains(t, ids, "b1")
}

func TestSelectTracks_DedupsSameTitleArtist(t *testing.T) {
	candidates := []taggedTrack{
		{ID: "1", Title: "Same Song", Artist: "Same Artist"},
		{ID: "2", Title: "same song", Artist: "same artist"}, // different casing, same track on another album
		{ID: "3", Title: "Different Song", Artist: "Same Artist"},
	}

	ids := selectTracks(candidates, 10, 0)

	require.Len(t, ids, 2)
}

func TestSelectTracks_RespectsLimit(t *testing.T) {
	var candidates []taggedTrack
	for i := 0; i < 20; i++ {
		candidates = append(candidates, taggedTrack{ID: string(rune('a' + i)), Title: "T", Artist: "Artist" + string(rune('a'+i))})
	}

	ids := selectTracks(candidates, 5, 0)

	require.Len(t, ids, 5)
}

// ── Tag discovery / fetch helpers ────────────────────────────────

func TestFetchAllUserTags(t *testing.T) {
	resetMocks()
	host.SubsonicAPIMock.On("Call", "getAllUserTags.view?").
		Return(`{"subsonic-response":{"userTags":{"tag":["genre:rock","mood:chill"]}}}`, nil).Once()

	tags, err := fetchAllUserTags()

	require.NoError(t, err)
	require.Equal(t, []string{"genre:rock", "mood:chill"}, tags)
}

func TestFetchSongsByTag(t *testing.T) {
	resetMocks()
	host.SubsonicAPIMock.On("Call", "getSongsByUserTag.view?tag=mood%3Achill").
		Return(`{"subsonic-response":{"songsByUserTag":{"song":[
			{"id":"t1","title":"Song One","artist":"Artist A"},
			{"id":"t2","title":"Song Two","artist":"Artist B"}
		]}}}`, nil).Once()

	tracks, err := fetchSongsByTag("mood:chill")

	require.NoError(t, err)
	require.Equal(t, []taggedTrack{
		{ID: "t1", Title: "Song One", Artist: "Artist A"},
		{ID: "t2", Title: "Song Two", Artist: "Artist B"},
	}, tracks)
}

// ── Rebuild pipeline ─────────────────────────────────────────────

func TestStartRebuild_FiltersToConfiguredCategoriesAndEnqueues(t *testing.T) {
	resetMocks()
	host.SubsonicAPIMock.On("Call", "getAllUserTags.view?").
		Return(`{"subsonic-response":{"userTags":{"tag":["genre:rock","mood:chill","language:english"]}}}`, nil).Once()
	host.ConfigMock.On("Get", "playlist_tag_categories").Return("genre,mood", true).Once()

	host.TaskMock.On("Enqueue", rebuildQueue, mock.MatchedBy(func(payload []byte) bool {
		var task map[string]string
		if err := json.Unmarshal(payload, &task); err != nil {
			return false
		}
		return task["tag"] == "genre:rock"
	})).Return("task-1", nil).Once()
	host.TaskMock.On("Enqueue", rebuildQueue, mock.MatchedBy(func(payload []byte) bool {
		var task map[string]string
		if err := json.Unmarshal(payload, &task); err != nil {
			return false
		}
		return task["tag"] == "mood:chill"
	})).Return("task-2", nil).Once()

	err := startRebuild()

	require.NoError(t, err)
	host.TaskMock.AssertExpectations(t)
	host.TaskMock.AssertNotCalled(t, "Enqueue", rebuildQueue, mock.MatchedBy(func(payload []byte) bool {
		var task map[string]string
		json.Unmarshal(payload, &task)
		return task["tag"] == "language:english"
	}))
}

func TestStartRebuild_GenreAllowlistOnlyNarrowsGenres(t *testing.T) {
	resetMocks()
	host.ConfigMock.ExpectedCalls, host.ConfigMock.Calls = nil, nil // drop the blanket "unset" defaults below
	host.ConfigMock.On("Get", "navidrome_url").Return("", false).Maybe()
	host.ConfigMock.On("Get", "navidrome_user").Return("", false).Maybe()
	host.ConfigMock.On("Get", "navidrome_password").Return("", false).Maybe()
	host.ConfigMock.On("Get", "mood_allowlist").Return("", false).Maybe()

	host.SubsonicAPIMock.On("Call", "getAllUserTags.view?").
		Return(`{"subsonic-response":{"userTags":{"tag":["genre:rock","genre:electronic","mood:chill"]}}}`, nil).Once()
	host.ConfigMock.On("Get", "playlist_tag_categories").Return("genre,mood", true).Once()
	host.ConfigMock.On("Get", "genre_allowlist").Return(`["rock"]`, true).Once()

	host.TaskMock.On("Enqueue", rebuildQueue, mock.MatchedBy(func(payload []byte) bool {
		var task map[string]string
		json.Unmarshal(payload, &task)
		return task["tag"] == "genre:rock"
	})).Return("task-1", nil).Once()
	// mood_allowlist is unset, so mood:chill is unaffected by the genre allowlist.
	host.TaskMock.On("Enqueue", rebuildQueue, mock.MatchedBy(func(payload []byte) bool {
		var task map[string]string
		json.Unmarshal(payload, &task)
		return task["tag"] == "mood:chill"
	})).Return("task-2", nil).Once()

	err := startRebuild()

	require.NoError(t, err)
	host.TaskMock.AssertExpectations(t)
	host.TaskMock.AssertNotCalled(t, "Enqueue", rebuildQueue, mock.MatchedBy(func(payload []byte) bool {
		var task map[string]string
		json.Unmarshal(payload, &task)
		return task["tag"] == "genre:electronic"
	}))
}

func TestRebuildTagPlaylist_SelectsAndUpsertsPlaylist(t *testing.T) {
	resetMocks()
	host.SubsonicAPIMock.On("Call", "getSongsByUserTag.view?tag=mood%3Achill").
		Return(`{"subsonic-response":{"songsByUserTag":{"song":[
			{"id":"t1","title":"Song One","artist":"Artist A"},
			{"id":"t2","title":"Song Two","artist":"Artist B"}
		]}}}`, nil).Once()
	host.ConfigMock.On("GetInt", "playlist_size").Return(int64(50), true).Once()
	host.ConfigMock.On("GetInt", "max_tracks_per_artist").Return(int64(3), true).Once()
	host.SubsonicAPIMock.On("Call", "getPlaylists.view?").
		Return(`{"subsonic-response":{"playlists":{"playlist":[]}}}`, nil).Times(2)
	host.ConfigMock.On("Get", "show_dates_in_title").Return("false", true)
	host.SubsonicAPIMock.On("Call", mock.MatchedBy(func(uri string) bool {
		return strings.HasPrefix(uri, "createPlaylist?")
	})).Return(`{"subsonic-response":{}}`, nil).Once()

	err := rebuildTagPlaylist("mood:chill")

	require.NoError(t, err)
	host.SubsonicAPIMock.AssertExpectations(t)
}

func TestRebuildTagPlaylist_NoTracksIsNotAnError(t *testing.T) {
	resetMocks()
	host.SubsonicAPIMock.On("Call", "getSongsByUserTag.view?tag=mood%3Aobscure").
		Return(`{"subsonic-response":{"songsByUserTag":{}}}`, nil).Once()

	err := rebuildTagPlaylist("mood:obscure")

	require.NoError(t, err)
}

func TestOnTaskExecute_ParsesPayloadAndRebuilds(t *testing.T) {
	resetMocks()
	host.SubsonicAPIMock.On("Call", "getSongsByUserTag.view?tag=genre%3Arock").
		Return(`{"subsonic-response":{"songsByUserTag":{"song":[{"id":"t1","title":"T","artist":"A"}]}}}`, nil).Once()
	host.ConfigMock.On("GetInt", "playlist_size").Return(int64(50), true)
	host.ConfigMock.On("GetInt", "max_tracks_per_artist").Return(int64(3), true)
	host.SubsonicAPIMock.On("Call", "getPlaylists.view?").
		Return(`{"subsonic-response":{"playlists":{"playlist":[]}}}`, nil)
	host.ConfigMock.On("Get", "show_dates_in_title").Return("false", true)
	host.SubsonicAPIMock.On("Call", mock.MatchedBy(func(uri string) bool {
		return strings.HasPrefix(uri, "createPlaylist")
	})).Return(`{"subsonic-response":{}}`, nil)

	payload, _ := json.Marshal(map[string]string{"tag": "genre:rock"})

	result, err := (&moodPlugin{}).OnTaskExecute(taskworker.TaskExecuteRequest{
		QueueName: rebuildQueue,
		TaskID:    "task-1",
		Payload:   payload,
		Attempt:   1,
	})

	require.NoError(t, err)
	require.Equal(t, "rebuilt genre:rock", result)
}

func TestOnTaskExecute_InvalidPayloadErrors(t *testing.T) {
	resetMocks()

	_, err := (&moodPlugin{}).OnTaskExecute(taskworker.TaskExecuteRequest{Payload: []byte("not json")})

	require.Error(t, err)
}

// ── Instant Mix ──────────────────────────────────────────────────

func TestGetSimilarSongsByTrack_RanksByTagOverlap(t *testing.T) {
	resetMocks()
	host.SubsonicAPIMock.On("Call", "getUserTags.view?id=source").
		Return(`{"subsonic-response":{"userTags":{"tag":["genre:rock","mood:energetic"]}}}`, nil).Once()
	host.SubsonicAPIMock.On("Call", "getSongsByUserTag.view?tag=genre%3Arock").
		Return(`{"subsonic-response":{"songsByUserTag":{"song":[
			{"id":"source","title":"Source Track","artist":"Me"},
			{"id":"t1","title":"Only Genre Match","artist":"A"},
			{"id":"t2","title":"Both Match","artist":"B"}
		]}}}`, nil).Once()
	host.SubsonicAPIMock.On("Call", "getSongsByUserTag.view?tag=mood%3Aenergetic").
		Return(`{"subsonic-response":{"songsByUserTag":{"song":[
			{"id":"t2","title":"Both Match","artist":"B"}
		]}}}`, nil).Once()

	p := &moodPlugin{}
	resp, err := p.GetSimilarSongsByTrack(metadata.SimilarSongsByTrackRequest{ID: "source", Count: 5})

	require.NoError(t, err)
	require.Len(t, resp.Songs, 2)
	require.Equal(t, "t2", resp.Songs[0].ID) // higher overlap (2 tags) ranks first
	require.Equal(t, "t1", resp.Songs[1].ID)
}

func TestGetSimilarSongsByTrack_NoTagsReturnsEmpty(t *testing.T) {
	resetMocks()
	host.SubsonicAPIMock.On("Call", "getUserTags.view?id=untagged").
		Return(`{"subsonic-response":{"userTags":{"tag":[]}}}`, nil).Once()

	p := &moodPlugin{}
	resp, err := p.GetSimilarSongsByTrack(metadata.SimilarSongsByTrackRequest{ID: "untagged", Count: 5})

	require.NoError(t, err)
	require.Empty(t, resp.Songs)
}

// ── Lifecycle / scheduling ───────────────────────────────────────

func TestOnInit_SchedulesRebuildAndCreatesQueue(t *testing.T) {
	resetMocks()
	host.ConfigMock.On("Get", "rebuild_schedule").Return("0 * * * *", true).Once()
	host.SchedulerMock.On("ScheduleRecurring", "0 * * * *", rebuildPayload, "mood-rebuild-schedule").
		Return("mood-rebuild-schedule", nil).Once()
	host.ConfigMock.On("Get", "enrich_playlists").Return("false", true).Once()
	host.TaskMock.On("ClearQueue", rebuildQueue).Return(int64(0), nil).Once()
	host.ConfigMock.On("GetInt", "max_concurrency").Return(int64(2), true).Once()
	host.TaskMock.On("CreateQueue", rebuildQueue, host.QueueConfig{
		Concurrency: 2, MaxRetries: 3, BackoffMs: 10_000,
	}).Return(nil).Once()

	err := (&moodPlugin{}).OnInit()

	require.NoError(t, err)
	host.SchedulerMock.AssertExpectations(t)
	host.TaskMock.AssertExpectations(t)
}

func TestOnInit_PropagatesScheduleError(t *testing.T) {
	resetMocks()
	host.ConfigMock.On("Get", "rebuild_schedule").Return("", false).Once()
	host.SchedulerMock.On("ScheduleRecurring", "0 * * * *", rebuildPayload, "mood-rebuild-schedule").
		Return("", errors.New("scheduler unavailable")).Once()

	err := (&moodPlugin{}).OnInit()

	require.Error(t, err)
}

func TestOnCallback_DispatchesRebuild(t *testing.T) {
	resetMocks()
	host.SubsonicAPIMock.On("Call", "getAllUserTags.view?").
		Return(`{"subsonic-response":{"userTags":{"tag":[]}}}`, nil).Once()
	host.ConfigMock.On("Get", "playlist_tag_categories").Return("genre,mood", true).Once()

	err := (&moodPlugin{}).OnCallback(scheduler.SchedulerCallbackRequest{Payload: rebuildPayload})

	require.NoError(t, err)
}

func TestOnCallback_UnknownPayloadIsHarmless(t *testing.T) {
	resetMocks()

	err := (&moodPlugin{}).OnCallback(scheduler.SchedulerCallbackRequest{Payload: "something-unexpected"})

	require.NoError(t, err)
}

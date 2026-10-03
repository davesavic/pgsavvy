package common

import (
	"bytes"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// --- fake clock --------------------------------------------------------------

// fakeTimer is a Timer whose firing is controlled by the fake clock's Advance.
type fakeTimer struct {
	clk      *fakeClock
	deadline time.Time
	fn       func()
	stopped  bool
	fired    bool
	idx      int // index into clk.timers
}

func (t *fakeTimer) Stop() bool {
	t.clk.mu.Lock()
	defer t.clk.mu.Unlock()
	if t.fired || t.stopped {
		return false
	}
	t.stopped = true
	return true
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, fn func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{
		clk:      c,
		deadline: c.now.Add(d),
		fn:       fn,
		idx:      len(c.timers),
	}
	c.timers = append(c.timers, t)
	return t
}

// Advance moves the clock forward by d, firing (synchronously, in order) any
// non-stopped timer whose deadline is reached. Timer fn is invoked WITHOUT
// holding c.mu so re-entrant AfterFunc calls from fn work.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	due := []*fakeTimer{}
	for _, t := range c.timers {
		if t.stopped || t.fired {
			continue
		}
		if !t.deadline.After(c.now) {
			t.fired = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.fn()
	}
}

// --- tests ------------------------------------------------------------------

func TestStoreRapidMashCoalesces(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	const path = "/state.yml"

	// Recording Fs: count Rename calls (atomic-rename is the commit point).
	rec := &recordingFs{Fs: fs}
	s := NewAppStateStore(rec, path, clk)
	defer func() { _ = s.Close() }()

	// 50 mutations in a tight loop, all within "100ms" of fake time. Advance
	// clock 1ms between each so timers don't all share an identical deadline
	// (production usage spaces these via real keystrokes).
	for i := range 50 {
		idx := i
		s.MutateAndSave(func(a *AppState) {
			if a.HiddenSchemas == nil {
				a.HiddenSchemas = map[string][]string{}
			}
			a.HiddenSchemas["conn"] = []string{"v" + intToStr(idx)}
		})
		clk.Advance(1 * time.Millisecond)
	}
	// Total elapsed so far: 50ms. No save should have fired yet (debounce is
	// 500ms after the LAST mutation).
	require.Equal(t, int32(0), rec.renames.Load(), "no save expected before debounce window elapses")

	// Advance past the 500ms debounce window.
	clk.Advance(DebounceWindow + 10*time.Millisecond)

	// Flush in case the debounced fire is still draining (it's synchronous in
	// our fake clock so this is effectively a no-op, but assert the contract).
	require.NoError(t, s.Flush())

	require.Equal(t, int32(1), rec.renames.Load(), "exactly one Save expected after debounce")

	// On-disk state reflects the LAST mutation.
	b := &AppState{}
	require.NoError(t, b.Load(fs, path))
	require.Equal(t, []string{"v49"}, b.HiddenSchemas["conn"])
}

func TestCloseDrainsInFlight(t *testing.T) {
	// Capture goroutine count baseline.
	before := runtime.NumGoroutine()

	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)

	s.MutateAndSave(func(a *AppState) { a.LastConnectionID = "x" })

	deadline := time.Now().Add(100 * time.Millisecond)
	require.NoError(t, s.Close())
	require.True(t, time.Now().Before(deadline), "Close should return within 100ms")

	// After Close, MutateAndSave is a no-op (and records errStoreClosed).
	s.MutateAndSave(func(a *AppState) { a.LastConnectionID = "y" })
	require.ErrorIs(t, s.LastSaveErr(), errStoreClosed)

	// Idempotent.
	require.NoError(t, s.Close())

	// Goroutine hygiene: AppStateStore must not leak background goroutines.
	// We don't use go.uber.org/goleak yet (lands in T11) — assert NumGoroutine
	// returns to baseline within a tolerance.
	runtime.Gosched()
	after := runtime.NumGoroutine()
	require.LessOrEqual(t, after, before+1, "no goroutine leak: before=%d after=%d", before, after)
}

func TestSaveSnapshotIsolation(t *testing.T) {
	// Concurrent MutateAndSave + background synchronous Save must be -race
	// clean. The snapshot pattern in saveSnapshot is what makes this safe:
	// yaml.Marshal sees a defensive deep copy, not the live state.
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)
	defer func() { _ = s.Close() }()

	var wg sync.WaitGroup
	const iters = 200

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range iters {
			idx := i
			s.MutateAndSave(func(a *AppState) {
				if a.HiddenSchemas == nil {
					a.HiddenSchemas = map[string][]string{}
				}
				a.HiddenSchemas["k"] = []string{"v" + intToStr(idx)}
				if a.CanonicalSessionSettings == nil {
					a.CanonicalSessionSettings = &CanonicalSessionSettingsState{Version: 1, Connections: map[string]map[string]CanonicalSessionSetting{"k": {}}}
				}
				a.CanonicalSessionSettings.Connections["k"]["application_name"] = CanonicalSessionSetting{Values: []string{"v" + intToStr(idx)}, Provenance: "decoded"}
			})
		}
	}()
	go func() {
		defer wg.Done()
		for range iters {
			_ = s.Save()
		}
	}()
	wg.Wait()
}

func TestFlushWaitsForPendingSave(t *testing.T) {
	// With the fake clock the debouncedFire is invoked synchronously by
	// clk.Advance — so Flush after Advance is trivially satisfied. Verify the
	// contract: with NO Advance, Flush blocks until... we Close (which clears
	// pending). Use a goroutine + channel to assert ordering.
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)

	s.MutateAndSave(func(a *AppState) { a.LastConnectionID = "x" })

	done := make(chan struct{})
	go func() {
		_ = s.Flush()
		close(done)
	}()

	// Give the goroutine a moment to enter Flush.
	runtime.Gosched()

	select {
	case <-done:
		t.Fatal("Flush returned before pending save fired")
	default:
	}

	// Advance the clock past the debounce window; the timer fires
	// synchronously inside Advance, marks pending=false, broadcasts.
	clk.Advance(DebounceWindow + time.Millisecond)

	select {
	case <-done:
		// Expected: Flush released after debouncedFire cleared pending.
	case <-time.After(time.Second):
		t.Fatal("Flush did not return after debounce window elapsed")
	}

	// Subsequent Flush returns immediately.
	require.NoError(t, s.Flush())
	require.NoError(t, s.Close())
}

func TestIsStartupTipsSeenAndStamp(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)
	defer func() { _ = s.Close() }()

	require.False(t, s.IsStartupTipsSeen(), "fresh store: tips not seen")

	s.StampStartupTips()
	require.True(t, s.IsStartupTipsSeen(), "after StampStartupTips: tips seen")

	// StampStartupTips schedules a debounced save; advance the clock so it
	// fires, then verify on-disk state.
	clk.Advance(DebounceWindow + time.Millisecond)
	require.NoError(t, s.Flush())

	b := &AppState{}
	require.NoError(t, b.Load(fs, "/state.yml"))
	require.False(t, b.StartupTipsSeenAt.IsZero(), "persisted timestamp non-zero")
}

// TestStoreVersionTracking covers IsVersionChanged and StampVersion:
// fresh install (empty version) must report changed, dev builds must
// always report unchanged, stamp must persist and survive a reload.
func TestStoreVersionTracking(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)
	defer func() { _ = s.Close() }()

	// Fresh install: Version is empty, must report changed.
	require.True(t, s.IsVersionChanged("v1.0.0"), "fresh install must register as version changed")

	// Dev build: must always return false.
	require.False(t, s.IsVersionChanged("dev"), "dev version must never report changed")
	require.False(t, s.IsVersionChanged(""), "empty build version must never report changed")

	// Stamp the current version.
	s.StampVersion("v1.0.0")
	clk.Advance(DebounceWindow + time.Millisecond)
	require.NoError(t, s.Flush())

	// After stamp, IsVersionChanged must return false for the same version.
	require.False(t, s.IsVersionChanged("v1.0.0"), "same version must not report changed after stamp")

	// Different version must report changed.
	require.True(t, s.IsVersionChanged("v2.0.0"), "different version must report changed")

	// Dev build stamp must be a no-op — version stays v1.0.0.
	s.StampVersion("dev")
	clk.Advance(DebounceWindow + time.Millisecond)
	require.NoError(t, s.Flush())
	require.False(t, s.IsVersionChanged("v1.0.0"), "dev stamp must not overwrite real version")

	// Reload and verify persistence.
	s2 := NewAppStateStore(fs, "/state.yml", clk)
	defer func() { _ = s2.Close() }()
	require.NoError(t, s2.Load())
	require.False(t, s2.IsVersionChanged("v1.0.0"), "version must persist across reload")
	require.True(t, s2.IsVersionChanged("v2.0.0"), "version change must survive reload")
}

// TestStoreGetOrCreateBufferUUID_PersistsAcrossLoad is the regression test:
// the editor buffer UUID must round-trip through disk so
// the same .sql file is reused on the next run. Before the fix the UUID was
// minted into Common.AppState (an unwired phantom) and never reached
// last_buffer_uuids on disk, so each startup generated a fresh UUID and
// orphaned the previous buffer file.
func TestStoreGetOrCreateBufferUUID_PersistsAcrossLoad(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	const connID = "localhost"

	s := NewAppStateStore(fs, "/state.yml", clk)
	first := s.GetOrCreateBufferUUID(connID)
	require.NotEmpty(t, first, "first call must generate a UUID")

	// Idempotent within the same store instance.
	require.Equal(t, first, s.GetOrCreateBufferUUID(connID), "same connID returns same UUID")

	// Flush the debounced save and tear down the store, simulating app exit.
	clk.Advance(DebounceWindow + time.Millisecond)
	require.NoError(t, s.Flush())
	require.NoError(t, s.Close())

	// Fresh store + Load() — simulates the next app start. The previously
	// minted UUID must come back so LoadBuffer reads the persisted .sql file
	// rather than orphaning it.
	s2 := NewAppStateStore(fs, "/state.yml", clk)
	defer func() { _ = s2.Close() }()
	require.NoError(t, s2.Load())
	require.Equal(t, first, s2.GetOrCreateBufferUUID(connID),
		"UUID must persist across store reload")
}

func TestStoreGetOrCreateBufferUUID_EmptyConnID(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)
	defer func() { _ = s.Close() }()

	require.Empty(t, s.GetOrCreateBufferUUID(""), "empty connID returns empty UUID")
}

func TestLastConnectionIDSnapshot(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)
	defer func() { _ = s.Close() }()

	require.Empty(t, s.LastConnectionIDSnapshot(), "fresh store returns empty")

	s.MutateAndSave(func(a *AppState) { a.LastConnectionID = "my-db" })
	require.Equal(t, "my-db", s.LastConnectionIDSnapshot())
}

func TestLastSchemaNameSnapshot(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)
	defer func() { _ = s.Close() }()

	require.Empty(t, s.LastSchemaNameSnapshot("conn1"), "fresh store returns empty")

	s.SetLastSchemaName("conn1", "public")
	require.Equal(t, "public", s.LastSchemaNameSnapshot("conn1"))
	require.Empty(t, s.LastSchemaNameSnapshot("conn2"), "different connID returns empty")
	require.Empty(t, s.LastSchemaNameSnapshot(""), "empty connID returns empty")
}

func TestLastTableNameSnapshot(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)
	defer func() { _ = s.Close() }()

	require.Empty(t, s.LastTableNameSnapshot("conn1"), "fresh store returns empty")

	s.SetLastTableName("conn1", "users")
	require.Equal(t, "users", s.LastTableNameSnapshot("conn1"))
	require.Empty(t, s.LastTableNameSnapshot("conn2"), "different connID returns empty")
}

func TestReview_picker_state(t *testing.T) {
	for _, mode := range []string{"synchronous", "debounced"} {
		for _, directory := range []string{"", "/remembered/資料"} {
			for _, failSave := range []bool{false, true} {
				name := mode + "/populated"
				if directory == "" {
					name = mode + "/empty"
				}
				if failSave {
					name += "/failed_save"
				} else {
					name += "/saved"
				}
				t.Run(name, func(t *testing.T) {
					fs := afero.NewMemMapFs()
					const path = "/state.yml"
					seed := &AppState{LastPickerDirectory: directory, LastConnectionID: "before"}
					require.NoError(t, seed.Save(fs, path))
					require.NoError(t, seed.Load(fs, path))
					before, err := afero.ReadFile(fs, path)
					require.NoError(t, err)
					storeFs := fs
					if failSave {
						storeFs = afero.NewReadOnlyFs(fs)
					}
					clk := newFakeClock()
					s := NewAppStateStore(storeFs, path, clk)
					t.Cleanup(func() { _ = s.Close() })
					var logOutput bytes.Buffer
					s.SetLogger(slog.New(slog.NewJSONHandler(&logOutput, &slog.HandlerOptions{Level: slog.LevelDebug})))
					require.NoError(t, s.Load())
					s.MutateAndSave(func(a *AppState) {
						a.LastConnectionID = "after"
						a.LastResultViewMode = "expanded"
					})
					if mode == "synchronous" {
						err = s.Save()
						// Synchronous Save returns its error without recording LastSaveErr.
						require.NoError(t, s.LastSaveErr())
					} else {
						clk.Advance(DebounceWindow - time.Millisecond)
						stillBefore, readErr := afero.ReadFile(fs, path)
						require.NoError(t, readErr)
						require.Equal(t, before, stillBefore)
						clk.Advance(time.Millisecond)
						err = s.Flush()
						require.Equal(t, err, s.LastSaveErr())
					}
					if failSave {
						require.Error(t, err)
						after, readErr := afero.ReadFile(fs, path)
						require.NoError(t, readErr)
						require.Equal(t, before, after, "failed save must not replace existing state")
						if mode == "debounced" {
							require.Contains(t, logOutput.String(), `"evt":"appstate_save_fire"`)
							require.NotContains(t, logOutput.String(), `"err":null`)
						}
					} else {
						require.NoError(t, err)
					}
					s.mu.Lock()
					liveDirectory := s.state.LastPickerDirectory
					s.mu.Unlock()
					require.Equal(t, directory, liveDirectory, "saving must not mutate the live directory")
					reloaded := &AppState{}
					require.NoError(t, reloaded.Load(fs, path))
					require.Equal(t, directory, reloaded.LastPickerDirectory)
					if failSave {
						require.Equal(t, seed, reloaded)
					} else {
						require.Equal(t, "after", reloaded.LastConnectionID)
						require.Equal(t, "expanded", reloaded.LastResultViewMode)
					}
				})
			}
		}
	}
}

func TestReview_picker_state_copy(t *testing.T) {
	orig := &AppState{
		LastConnectionID:         "connection",
		RecentConnectionIDs:      []string{"connection"},
		LastBufferUUIDs:          map[string]string{"connection": "buffer"},
		LastTheme:                "dark",
		LastResultViewMode:       "expanded",
		StartupTipsSeenAt:        time.Unix(1_700_000_000, 0).UTC(),
		Version:                  "v1.2.3",
		StatementTimeoutOverride: map[string]string{"connection": "1000"},
		HiddenSchemas:            map[string][]string{"connection": {"private"}},
		HiddenColumns:            map[string]map[string][]string{"connection": {"users": {"hidden"}}},
		LastSessionSettings:      map[string]map[string]string{"connection": {"application_name": "test"}},
		LastSchemaName:           map[string]string{"connection": "public"},
		LastTableName:            map[string]string{"connection": "users"},
		LastPickerDirectory:      "/remembered/資料",
		CanonicalSessionSettings: &CanonicalSessionSettingsState{Version: 1, Connections: map[string]map[string]CanonicalSessionSetting{"connection": {"search_path": {Values: []string{"schema,comma", "public"}, Provenance: "decoded"}}}},
	}
	cp := deepCopyAppState(orig)
	require.Equal(t, orig, cp, "populated snapshots must retain every field")
	cp.RecentConnectionIDs[0] = "changed"
	cp.LastBufferUUIDs["connection"] = "changed"
	cp.StatementTimeoutOverride["connection"] = "changed"
	cp.HiddenSchemas["connection"][0] = "changed"
	cp.HiddenColumns["connection"]["users"][0] = "changed"
	cp.LastSessionSettings["connection"]["application_name"] = "changed"
	cp.LastSchemaName["connection"] = "changed"
	cp.LastTableName["connection"] = "changed"
	cp.CanonicalSessionSettings.Version = 99
	cp.CanonicalSessionSettings.Connections["connection"]["search_path"].Values[0] = "changed"
	cp.CanonicalSessionSettings.Connections["connection"]["timezone"] = CanonicalSessionSetting{Values: []string{"changed"}, Provenance: "decoded"}
	cp.CanonicalSessionSettings.Connections["other"] = map[string]CanonicalSessionSetting{}
	require.Equal(t, []string{"connection"}, orig.RecentConnectionIDs)
	require.Equal(t, "buffer", orig.LastBufferUUIDs["connection"])
	require.Equal(t, "1000", orig.StatementTimeoutOverride["connection"])
	require.Equal(t, []string{"private"}, orig.HiddenSchemas["connection"])
	require.Equal(t, []string{"hidden"}, orig.HiddenColumns["connection"]["users"])
	require.Equal(t, "test", orig.LastSessionSettings["connection"]["application_name"])
	require.Equal(t, "public", orig.LastSchemaName["connection"])
	require.Equal(t, "users", orig.LastTableName["connection"])
	require.Equal(t, 1, orig.CanonicalSessionSettings.Version)
	require.Equal(t, map[string]map[string]CanonicalSessionSetting{"connection": {"search_path": {Values: []string{"schema,comma", "public"}, Provenance: "decoded"}}}, orig.CanonicalSessionSettings.Connections)
	require.Equal(t, &AppState{}, deepCopyAppState(nil))
	require.Equal(t, &AppState{}, deepCopyAppState(&AppState{}))
}

func TestDeepCopyIsolatesNewMapFields(t *testing.T) {
	orig := &AppState{
		LastSchemaName: map[string]string{"c1": "public"},
		LastTableName:  map[string]string{"c1": "users"},
	}
	cp := deepCopyAppState(orig)

	cp.LastSchemaName["c1"] = "modified"
	cp.LastTableName["c1"] = "modified"

	require.Equal(t, "public", orig.LastSchemaName["c1"], "deep copy must isolate LastSchemaName")
	require.Equal(t, "users", orig.LastTableName["c1"], "deep copy must isolate LastTableName")
}

func TestSchemaTableNameRoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)

	s.SetLastSchemaName("conn1", "public")
	s.SetLastTableName("conn1", "users")

	clk.Advance(DebounceWindow + time.Millisecond)
	require.NoError(t, s.Flush())
	require.NoError(t, s.Close())

	s2 := NewAppStateStore(fs, "/state.yml", clk)
	defer func() { _ = s2.Close() }()
	require.NoError(t, s2.Load())

	require.Equal(t, "public", s2.LastSchemaNameSnapshot("conn1"))
	require.Equal(t, "users", s2.LastTableNameSnapshot("conn1"))
}

func TestMutateAfterCloseReturnsErr(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)

	require.NoError(t, s.Close())

	// Should not panic; should set lastSaveErr.
	s.MutateAndSave(func(a *AppState) { a.LastConnectionID = "x" })
	require.ErrorIs(t, s.LastSaveErr(), errStoreClosed)
	require.ErrorIs(t, ErrStoreClosed(), errStoreClosed)
}

func TestReview_settings_storage_store(t *testing.T) {
	for _, mode := range []string{"synchronous", "debounced"} {
		t.Run(mode, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			clk := newFakeClock()
			s := NewAppStateStore(fs, "/state.yml", clk)
			t.Cleanup(func() { _ = s.Close() })
			missing, err := s.CanonicalSessionSettingsSnapshot("conn")
			require.NoError(t, err)
			require.Nil(t, missing)
			s.MutateAndSave(func(a *AppState) {
				a.LastSessionSettings = map[string]map[string]string{"conn": {"application_name": "stale", "statement_timeout": "100", "work_mem": "4MB"}, "other": {"application_name": "other"}}
				a.StatementTimeoutOverride = map[string]string{"conn": "200", "other": "300"}
			})
			value := CanonicalSessionSetting{Values: []string{"O'Reilly 資料"}, Provenance: "decoded"}
			require.NoError(t, s.SetCanonicalSessionSetting("conn", "application_name", value))
			value.Values[0] = "caller-mutation"
			require.NoError(t, s.SetCanonicalSessionSetting("conn", "search_path", CanonicalSessionSetting{Values: []string{"schema,comma", `schema"quoted`, "public"}, Provenance: "decoded"}))
			require.NoError(t, s.SetCanonicalSessionSetting("conn", "statement_timeout", CanonicalSessionSetting{Values: []string{"500"}, Provenance: "decoded"}))
			snapshot, err := s.CanonicalSessionSettingsSnapshot("conn")
			require.NoError(t, err)
			require.Equal(t, []string{"O'Reilly 資料"}, snapshot["application_name"].Values)
			snapshot["application_name"].Values[0] = "snapshot-mutation"
			snapshot["timezone"] = CanonicalSessionSetting{Values: []string{"UTC"}, Provenance: "decoded"}
			delete(snapshot, "search_path")
			fresh, err := s.CanonicalSessionSettingsSnapshot("conn")
			require.NoError(t, err)
			require.Equal(t, []string{"O'Reilly 資料"}, fresh["application_name"].Values)
			require.NotContains(t, fresh, "timezone")
			require.Equal(t, []string{"schema,comma", `schema"quoted`, "public"}, fresh["search_path"].Values)
			require.Equal(t, map[string]string{"work_mem": "4MB"}, s.LastSessionSettingsSnapshot("conn"))
			require.Empty(t, s.StatementTimeoutOverrideValue("conn"))
			require.Equal(t, "300", s.StatementTimeoutOverrideValue("other"))
			if mode == "synchronous" {
				require.NoError(t, s.Save())
			} else {
				clk.Advance(DebounceWindow)
				require.NoError(t, s.Flush())
			}
			reloaded := NewAppStateStore(fs, "/state.yml", newFakeClock())
			t.Cleanup(func() { _ = reloaded.Close() })
			require.NoError(t, reloaded.Load())
			got, err := reloaded.CanonicalSessionSettingsSnapshot("conn")
			require.NoError(t, err)
			require.Equal(t, fresh, got)
			missing, err = reloaded.CanonicalSessionSettingsSnapshot("other")
			require.NoError(t, err)
			require.Nil(t, missing)
			require.NoError(t, reloaded.DeleteSessionSetting("conn", "statement_timeout"))
			require.NoError(t, reloaded.DeleteSessionSetting("conn", "work_mem"))
			require.NoError(t, reloaded.DeleteSessionSetting("other", "statement_timeout"))
			require.NoError(t, reloaded.Save())
			var disk AppState
			require.NoError(t, disk.Load(fs, "/state.yml"))
			require.NotContains(t, disk.CanonicalSessionSettings.Connections["conn"], "statement_timeout")
			require.Empty(t, disk.LastSessionSettings["conn"])
			require.Empty(t, disk.StatementTimeoutOverride)
			require.Equal(t, map[string]string{"application_name": "other"}, disk.LastSessionSettings["other"])
		})
	}
}

func TestReview_settings_storage_rejected_writes(t *testing.T) {
	for _, tc := range []struct {
		name, connID, key string
		value             CanonicalSessionSetting
	}{
		{"empty_connection", "", "timezone", CanonicalSessionSetting{Values: []string{"UTC"}, Provenance: "decoded"}},
		{"empty_key", "conn", "", CanonicalSessionSetting{Values: []string{"UTC"}, Provenance: "decoded"}},
		{"noncanonical_key", "conn", "TimeZone", CanonicalSessionSetting{Values: []string{"UTC"}, Provenance: "decoded"}},
		{"sql_key", "conn", "timezone;RESET ALL", CanonicalSessionSetting{Values: []string{"UTC"}, Provenance: "decoded"}},
		{"raw_provenance", "conn", "timezone", CanonicalSessionSetting{Values: []string{"'UTC'"}, Provenance: "sql"}},
		{"missing_value", "conn", "timezone", CanonicalSessionSetting{Provenance: "decoded"}},
		{"multiple_scalar_values", "conn", "timezone", CanonicalSessionSetting{Values: []string{"UTC", "GMT"}, Provenance: "decoded"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := newFakeClock()
			s := NewAppStateStore(afero.NewMemMapFs(), "/state.yml", clk)
			t.Cleanup(func() { _ = s.Close() })
			require.ErrorIs(t, s.SetCanonicalSessionSetting(tc.connID, tc.key, tc.value), ErrInvalidCanonicalSessionSetting)
			require.Equal(t, &AppState{}, s.state)
			require.Empty(t, clk.timers)
			require.False(t, s.pending)
		})
	}
	s := NewAppStateStore(afero.NewMemMapFs(), "/state.yml", newFakeClock())
	require.NoError(t, s.SetCanonicalSessionSetting("conn", "application_name", CanonicalSessionSetting{Values: []string{""}, Provenance: "decoded"}))
	require.NoError(t, s.Close())
	require.ErrorIs(t, s.SetCanonicalSessionSetting("conn", "timezone", CanonicalSessionSetting{Values: []string{"UTC"}, Provenance: "decoded"}), ErrStoreClosed())
	require.ErrorIs(t, s.DeleteSessionSetting("conn", "application_name"), ErrStoreClosed())
}

func TestReview_settings_storage_load_write_barrier(t *testing.T) {
	for _, data := range []string{
		"canonical_session_settings:\n  version: 99\n",
		"canonical_session_settings:\n  version: 99\n  connections: incompatible-future-shape\n",
		"canonical_session_settings:\n  version: 1\n  connections: incompatible-shape\n",
		"broken: [",
		"canonical_session_settings:\n  version: 1\n  connections:\n    conn:\n      timezone:\n        values: []\n        provenance: decoded\n",
	} {
		t.Run(data, func(t *testing.T) {
			fs := &recordingFs{Fs: afero.NewMemMapFs()}
			require.NoError(t, afero.WriteFile(fs, "/state.yml", []byte(data), 0o600))
			clk := newFakeClock()
			s := NewAppStateStore(fs, "/state.yml", clk)
			t.Cleanup(func() { _ = s.Close() })
			loadErr := s.Load()
			require.Error(t, loadErr)
			if bytes.Contains([]byte(data), []byte("version: 99")) {
				require.True(t, errors.Is(loadErr, ErrUnsupportedSessionSettingsVersion), "check version before decoding an incompatible future payload")
			}
			require.ErrorIs(t, s.Save(), loadErr)
			require.ErrorIs(t, s.SetCanonicalSessionSetting("conn", "timezone", CanonicalSessionSetting{Values: []string{"UTC"}, Provenance: "decoded"}), loadErr)
			require.ErrorIs(t, s.DeleteSessionSetting("conn", "timezone"), loadErr)
			_, err := s.CanonicalSessionSettingsSnapshot("conn")
			require.ErrorIs(t, err, loadErr)
			called := false
			s.MutateAndSave(func(a *AppState) { called = true })
			require.False(t, called)
			require.Empty(t, clk.timers)
			require.ErrorIs(t, s.Flush(), loadErr)
			require.ErrorIs(t, s.LastSaveErr(), loadErr)
			require.Equal(t, int32(0), fs.renames.Load())
			still, err := afero.ReadFile(fs, "/state.yml")
			require.NoError(t, err)
			require.Equal(t, data, string(still))
			// An unrelated failing Load cannot clear the barrier.
			require.NoError(t, afero.WriteFile(fs, "/state.yml", []byte("broken: ["), 0o600))
			require.Error(t, s.Load())
			require.ErrorIs(t, s.Save(), loadErr)
			require.NoError(t, afero.WriteFile(fs, "/state.yml", []byte("last_connection_id: recovered\n"), 0o600))
			require.NoError(t, s.Load())
			require.NoError(t, s.LastSaveErr())
			require.NoError(t, s.SetCanonicalSessionSetting("conn", "timezone", CanonicalSessionSetting{Values: []string{"UTC"}, Provenance: "decoded"}))
			clk.Advance(DebounceWindow)
			require.NoError(t, s.Flush())
			require.Equal(t, "recovered", s.LastConnectionIDSnapshot())
		})
	}
}

func TestReview_settings_storage_save_failure_visibility(t *testing.T) {
	fs := afero.NewMemMapFs()
	seed := AppState{LastConnectionID: "before"}
	require.NoError(t, seed.Save(fs, "/state.yml"))
	before, err := afero.ReadFile(fs, "/state.yml")
	require.NoError(t, err)
	clk := newFakeClock()
	s := NewAppStateStore(afero.NewReadOnlyFs(fs), "/state.yml", clk)
	t.Cleanup(func() { _ = s.Close() })
	var output bytes.Buffer
	s.SetLogger(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	require.NoError(t, s.Load())
	const sensitiveValue = "synthetic-secret-setting-value"
	require.NoError(t, s.SetCanonicalSessionSetting("conn", "application_name", CanonicalSessionSetting{Values: []string{sensitiveValue}, Provenance: "decoded"}))
	require.Error(t, s.Save())
	require.NoError(t, s.LastSaveErr(), "synchronous error contract unchanged")
	clk.Advance(DebounceWindow)
	require.Error(t, s.Flush())
	require.Error(t, s.LastSaveErr())
	require.Contains(t, output.String(), `"evt":"appstate_save_fire"`)
	require.NotContains(t, output.String(), `"err":null`)
	require.NotContains(t, output.String(), sensitiveValue)
	after, err := afero.ReadFile(fs, "/state.yml")
	require.NoError(t, err)
	require.Equal(t, before, after)
}

type stateReadFailureFs struct{ afero.Fs }

func (f *stateReadFailureFs) Open(string) (afero.File, error) {
	return nil, errors.New("synthetic state read failure")
}

func TestReview_settings_storage_read_failure_barrier(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/state.yml"
	before := []byte("last_connection_id: preserved\n")
	require.NoError(t, afero.WriteFile(fs, path, before, 0o600))
	clk := newFakeClock()
	s := NewAppStateStore(&stateReadFailureFs{Fs: fs}, path, clk)
	t.Cleanup(func() { _ = s.Close() })
	loadErr := s.Load()
	require.ErrorContains(t, loadErr, "synthetic state read failure")
	require.ErrorIs(t, s.Save(), loadErr)
	s.MutateAndSave(func(a *AppState) { t.Fatal("blocked mutation executed") })
	require.Empty(t, clk.timers)
	after, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	s.mu.Lock()
	s.fs = fs
	s.mu.Unlock()
	require.NoError(t, s.Load())
	require.Equal(t, "preserved", s.LastConnectionIDSnapshot())
	require.NoError(t, s.Save())
}

// --- helpers ----------------------------------------------------------------

// recordingFs counts atomic-rename calls so a test can assert "exactly N
// successful Save() commits hit the disk". All other operations delegate to
// the embedded base.
type recordingFs struct {
	afero.Fs
	renames atomic.Int32
}

func (r *recordingFs) Rename(oldname, newname string) error {
	r.renames.Add(1)
	return r.Fs.Rename(oldname, newname)
}

func (r *recordingFs) Name() string { return "recordingFs" }

// intToStr is a tiny stdlib-free int formatter to keep the test file's import
// surface minimal (we already pull in testify + afero).
func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}

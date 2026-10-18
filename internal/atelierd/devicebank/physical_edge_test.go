package devicebank

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFailedProbeKeepsPhysicalLease(t *testing.T) {
	f := useFake(t)
	c := useClock(t, t0)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)
	pause(t, "sess-1")

	f.physErr = errors.New("adb down")
	if id := leaseAny(t, "sess-1", PlatformAndroid); id != pixel.ID {
		t.Fatalf("a failed probe never ends a lease, got %q", id)
	}
	if l := load(t).FindLease("sess-1", PlatformAndroid); l == nil || l.Suspended() || f.stayOnOf(pixel.Serial) != "3" {
		t.Fatalf("the paused lease resumes even without a probe, got %+v stay_on %q", l, f.stayOnOf(pixel.Serial))
	}
	if len(load(t).Restores) != 0 {
		t.Fatal("no restore is started on a failed probe")
	}

	release(t, "sess-1")
	c.advance(restoreReapAfter + time.Minute)
	if err := Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := load(t)
	if len(s.Restores) != 1 || s.Restores[0].LastError == "" || !s.Restores[0].LastAttemptAt.Equal(t0.Add(restoreReapAfter+time.Minute)) {
		t.Fatalf("an unprobed device keeps its restore pending with the attempt recorded, got %+v", s.Restores)
	}
}

func TestNoTypeCallResumesHeldPhysical(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)
	pause(t, "sess-1")

	if id := lease(t, "sess-1", PlatformAndroid); id != pixel.ID {
		t.Fatalf("a held physical lease is returned to a call without --type, got %q", id)
	}
	if l := load(t).FindLease("sess-1", PlatformAndroid); l.Suspended() || f.stayOnOf(pixel.Serial) != "3" {
		t.Fatalf("and resumed, got %+v stay_on %q", l, f.stayOnOf(pixel.Serial))
	}

	f.plug()
	if id := lease(t, "sess-1", PlatformAndroid); id != emulator(1) {
		t.Fatalf("a call without --type checks the cable too, got %q", id)
	}
}

func TestTransientEmptyProbeKeepsLease(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)

	f.blindFor = 1
	if id := leaseAny(t, "sess-1", PlatformAndroid); id != pixel.ID {
		t.Fatalf("one empty probe, as right after an adb restart, does not end the lease, got %q", id)
	}
	if len(load(t).Restores) != 0 {
		t.Fatal("no restore is started")
	}
}

func TestUnpreparedLeaseIsPreparedAgain(t *testing.T) {
	for _, tt := range []struct {
		name     string
		app      AppInfo
		wantID   string
		prepared bool
	}{
		{"store build", AppInfo{Name: "Example"}, emulator(1), false},
		{"developer build", AppInfo{Developer: true}, pixel.ID, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := useFake(t)
			f.plug(pixel)
			f.stayOnVal[pixel.Serial] = "0"
			f.install(pixel.Serial, runApp, tt.app)
			s := bank(androidDevice(1))
			commitLease(s, &Candidate{Physical: &pixel}, "sess-1", "/wd", PlatformAndroid, time.Now()).AppID = runApp
			seed(t, s)

			if id := lease(t, "sess-1", PlatformAndroid); id != tt.wantID {
				t.Fatalf("a lease whose preparer died is checked again before it is handed back, got %q", id)
			}
			l := load(t).leaseForPhysical(&pixel)
			if tt.prepared != (l != nil && l.Snapshot != nil && f.stayOnOf(pixel.Serial) == "3") {
				t.Fatalf("prepared=%v, got lease %+v stay_on %q", tt.prepared, l, f.stayOnOf(pixel.Serial))
			}
			if !f.hasApp(pixel.Serial, runApp) {
				t.Fatal("the app is never touched")
			}
		})
	}
}

func TestSecondPreparerKeepsFirstSnapshot(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "3"
	before := "0"
	s := bank(androidDevice(1))
	l := commitLease(s, &Candidate{Physical: &pixel}, "sess-1", "/wd", PlatformAndroid, time.Now())
	l.AppID = runApp
	l.Snapshot = &PhysicalSnapshot{StayOnBefore: &before}
	seed(t, s)

	if skip, err := takePhysical(context.Background(), l, pixel); skip != "" || err != nil {
		t.Fatalf("a device already prepared is handed out, got %q %v", skip, err)
	}
	if got := *load(t).FindLease("sess-1", PlatformAndroid).Snapshot.StayOnBefore; got != "0" || f.count("setStayOn") != 0 {
		t.Fatalf("the first snapshot stays, got %q %v", got, f.calls)
	}
}

func TestPhysicalLeaseReleasedDuringTakeIsLost(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	seed(t, bank(androidDevice(1)))
	f.onApps = func() { release(t, "sess-1") }

	if _, _, err := acquireAs("sess-1", PlatformAndroid, RequireAny, runApp); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("a lease released while its device is prepared hands back no id, got %v", err)
	}
	if f.count("setStayOn") != 0 || f.stayOnOf(pixel.Serial) != "0" {
		t.Fatalf("the screen setting is never changed, got %v", f.calls)
	}
}

func TestStaleRestoreWorkerRecordsNothing(t *testing.T) {
	f := useFake(t)
	f.runSpawns = false
	f.plug(pixel)
	before := "0"
	s := bank(androidDevice(1))
	s.Restores = []*PendingRestore{{DeviceID: pixel.ID, Serial: pixel.Serial, Name: pixel.Name, Platform: PlatformAndroid,
		StartedAt: t0.Add(6 * time.Minute), Snapshot: &PhysicalSnapshot{StayOnBefore: &before}}}
	seed(t, s)

	if err := runRestore(context.Background(), pixel.ID, t0); err != nil {
		t.Fatal(err)
	}
	if err := RunRestore(context.Background(), "unknown"); err != nil {
		t.Fatal(err)
	}
	if r := load(t).Restores; len(f.calls) != 0 || len(r) != 1 || r[0].LastError != "" {
		t.Fatalf("a worker re-spawned since does the work, the stale one nothing, got %v %+v", f.calls, r)
	}
}

func TestPauseUnreachablePhysicalStillSuspends(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)

	f.fail["setStayOn"] = 1
	pause(t, "sess-1")
	s := load(t)
	if l := s.FindLease("sess-1", PlatformAndroid); l == nil || !l.Suspended() || len(s.Restores) != 0 {
		t.Fatalf("a device out of reach is suspended, its lease kept, got %+v %+v", l, s.Restores)
	}
	release(t, "sess-1")
	if got := f.stayOnOf(pixel.Serial); got != "0" {
		t.Fatalf("the return puts the value back, got %q", got)
	}
}

func TestResumeStayOnFailureWarns(t *testing.T) {
	f := useFake(t)
	f.plug(pixel)
	f.stayOnVal[pixel.Serial] = "0"
	seed(t, bank(androidDevice(1)))
	leaseAny(t, "sess-1", PlatformAndroid)
	pause(t, "sess-1")

	f.fail["setStayOn"] = 1
	id, stderr, err := acquireAs("sess-1", PlatformAndroid, RequireAny, runApp)
	if err != nil || id != pixel.ID || !strings.Contains(stderr, "resumed, but its screen may lock") {
		t.Fatalf("a refused stay_on on resume warns and keeps the device, got %q %v %q", id, err, stderr)
	}
	if l := load(t).FindLease("sess-1", PlatformAndroid); l.Suspended() {
		t.Fatal("the lease is resumed")
	}
}

func TestStatusRowsListsAbsentPhysical(t *testing.T) {
	f := useFake(t)
	f.plug(iphone)
	s := bank(androidDevice(1))
	commitLease(s, &Candidate{Physical: &pixel}, "sess-1", "/wd", PlatformAndroid, t0)
	s.Restores = []*PendingRestore{
		{DeviceID: iphone.ID, Serial: iphone.Serial, Name: iphone.Name, Platform: PlatformIOS, EndedAt: t0},
		{DeviceID: "P6SERIAL", Serial: "P6SERIAL", Name: "Pixel 6", Platform: PlatformAndroid, EndedAt: t0},
	}
	seed(t, s)

	rows := physicalRows(t)
	if len(rows) != 3 {
		t.Fatalf("one row per physical device, got %+v", rows)
	}
	if r := rows[pixel.ID]; !r.Absent || r.Lease == nil || r.State != StateOn {
		t.Fatalf("an unplugged leased Pixel is listed absent and on, got %+v", r)
	}
	if r := rows[iphone.ID]; r.Absent || r.Restore == nil {
		t.Fatalf("a connected iPhone owed its restore is listed once with it, got %+v", r)
	}
	if r := rows["P6SERIAL"]; !r.Absent || r.Restore == nil {
		t.Fatalf("an absent device owed its restore is listed, got %+v", r)
	}

	pause(t, "sess-1")
	if r := physicalRows(t)[pixel.ID]; r.State != StateOff {
		t.Fatalf("a suspended physical lease is off, got %+v", r)
	}
}

func physicalRows(t *testing.T) map[string]StatusRow {
	t.Helper()
	rows, _, err := StatusRows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]StatusRow{}
	for _, r := range rows {
		if r.Physical {
			if _, dup := out[r.ID]; dup {
				t.Fatalf("%s is listed twice", r.ID)
			}
			out[r.ID] = r
		}
	}
	return out
}

func TestStayOnValueAccepted(t *testing.T) {
	for v, want := range map[string]bool{"0": true, "3": true, "null": true, "-1": true, "WARNING: linker: x\n0": false, "": false, "3; reboot": false} {
		if got := stayOnValue.MatchString(v); got != want {
			t.Errorf("stayOnValue(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestAapt2PathPicksNewestBuildTools(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ANDROID_HOME", root)
	for _, v := range []string{"9.0.0", "33.0.1", "34.0.0"} {
		dir := filepath.Join(root, "build-tools", v)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "aapt2"), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := aapt2Path()
	if err != nil || !strings.HasSuffix(got, filepath.Join("34.0.0", "aapt2")) {
		t.Fatalf("the newest build-tools wins over a lexicographic order, got %q %v", got, err)
	}
}

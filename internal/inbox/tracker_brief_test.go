package inbox_test

import (
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/inbox"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func briefTracker(f *fakeStore, brief time.Duration) *inbox.Tracker {
	tr := newTracker(f)
	tr.HoldDown(brief)
	return tr
}

// An outage over within the window is one uptime_blip, not down and up.
func TestUptimeBlipWithinWindow(t *testing.T) {
	f := newFake(monitor)
	tr := briefTracker(f, time.Minute)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0, 0)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(20*time.Second), 0)
	none(t, f)
	tr.Tick(ctx, t0.Add(30*time.Second)) // inside the window: still held
	none(t, f)
	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(45*time.Second), 0)
	n := one(t, f, store.NotifyUptimeBlip)
	if n.Subject != origin || n.Fields["since"] != rfc(t0) || n.Fields["until"] != rfc(t0.Add(45*time.Second)) ||
		n.Fields["reason"] != "timeout" {
		t.Fatalf("uptime_blip = %+v", n)
	}
	if f.has(1, "uptime", origin) {
		t.Fatal("state kept after the blip")
	}
	tr.Tick(ctx, t0.Add(time.Hour))
	none(t, f)
}

// A failure past the window announces the down, and its recovery is up.
func TestUptimeDownPastWindow(t *testing.T) {
	f := newFake(monitor)
	tr := briefTracker(f, time.Minute)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0, 0)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(30*time.Second), 0)
	none(t, f)
	tr.Uptime(ctx, 1, origin, false, "status", "", t0.Add(time.Minute), 0)
	n := one(t, f, store.NotifyUptimeDown)
	if n.Fields["since"] != rfc(t0) || n.Fields["reason"] != "status" || !n.At.Equal(t0.Add(time.Minute)) {
		t.Fatalf("uptime_down = %+v", n)
	}
	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(90*time.Second), 0)
	one(t, f, store.NotifyUptimeUp)
}

// Checks spaced at least the window apart are not delayed: the second
// failure already comes after it.
func TestUptimeWindowNoDelayForSparseChecks(t *testing.T) {
	every := 2 * time.Minute
	f := newFake(monitor)
	tr := briefTracker(f, time.Minute)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0, every)
	none(t, f)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(every), every)
	one(t, f, store.NotifyUptimeDown)
}

// No window is today's behaviour: down at the second failure, then up.
func TestUptimeNoWindow(t *testing.T) {
	f := newFake(monitor)
	tr := briefTracker(f, 0)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0, 0)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(10*time.Second), 0)
	one(t, f, store.NotifyUptimeDown)
	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(20*time.Second), 0)
	one(t, f, store.NotifyUptimeUp)
}

// A held down whose next check does not come is announced by Tick once the
// window has passed, and survives a restart in between.
func TestUptimeHeldReleasedByTick(t *testing.T) {
	f := newFake(monitor)
	tr := briefTracker(f, 5*time.Minute)
	tr.Uptime(ctx, 1, origin, false, "refused", "", t0, 0)
	tr.Uptime(ctx, 1, origin, false, "refused", "", t0.Add(time.Minute), 0)
	none(t, f)

	tr = briefTracker(f, 5*time.Minute) // a restart: the state is read back
	tr.Tick(ctx, t0.Add(4*time.Minute))
	none(t, f)
	tr.Tick(ctx, t0.Add(5*time.Minute))
	n := one(t, f, store.NotifyUptimeDown)
	if n.Fields["since"] != rfc(t0) || n.Fields["reason"] != "refused" || !n.At.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("uptime_down = %+v", n)
	}
	tr.Uptime(ctx, 1, origin, false, "refused", "", t0.Add(6*time.Minute), 0)
	none(t, f)
	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(7*time.Minute), 0)
	one(t, f, store.NotifyUptimeUp)
}

// A single failure, never down, recovers silently whatever the window.
func TestUptimeSingleFailureSilentWithWindow(t *testing.T) {
	f := newFake(monitor)
	tr := briefTracker(f, time.Minute)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0, 0)
	tr.Tick(ctx, t0.Add(2*time.Minute))
	none(t, f)
	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(3*time.Minute), 0)
	none(t, f)
}

// A good check past the window, before a tick released the held down, is
// not a blip: the outage is told as down, then up.
func TestUptimeRecoveryPastWindowBeforeTick(t *testing.T) {
	f := newFake(monitor)
	tr := briefTracker(f, time.Minute)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0, 0)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(40*time.Second), 0)
	none(t, f)
	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(90*time.Second), 0)
	got := f.take()
	if len(got) != 2 || got[0].Type != store.NotifyUptimeDown || got[1].Type != store.NotifyUptimeUp ||
		got[1].Fields["since"] != rfc(t0) || got[1].Fields["until"] != rfc(t0.Add(90*time.Second)) {
		t.Fatalf("sent %+v, want uptime_down then uptime_up", got)
	}
	if f.has(1, "uptime", origin) {
		t.Fatal("state kept after recovery")
	}
}

// A blip whose write fails is still the end of that outage: no tick and no
// later single failure turns it into a down.
func TestUptimeFailedBlipWriteForgetsOutage(t *testing.T) {
	f := newFake(monitor)
	tr := briefTracker(f, time.Minute)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0, 0)
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(10*time.Second), 0)
	f.failPut = true
	tr.Uptime(ctx, 1, origin, true, "", "", t0.Add(20*time.Second), 0)
	f.failPut = false
	none(t, f)
	tr.Tick(ctx, t0.Add(2*time.Minute))
	tr.Uptime(ctx, 1, origin, false, "timeout", "", t0.Add(3*time.Minute), 0)
	none(t, f)
}

// While a down is held its certificate waits, as it does while down.
func TestCertWaitsWhileDownHeld(t *testing.T) {
	f := newFake(monitor)
	tr := briefTracker(f, time.Minute)
	tr.Uptime(ctx, 1, origin, false, "tls", "", t0, 0)
	tr.Uptime(ctx, 1, origin, false, "tls", "", t0.Add(10*time.Second), 0)
	tr.Cert(ctx, 1, origin, inbox.CertReading{Read: true, Error: "x509: unknown authority"}, t0.Add(15*time.Second))
	none(t, f)
}

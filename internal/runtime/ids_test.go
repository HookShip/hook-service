package runtime_test

import (
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/runtime"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func TestULIDSourceMonotonicWithinMillisecond(t *testing.T) {
	source := runtime.NewULIDSource(fixedClock{t: time.UnixMilli(1_700_000_000_000).UTC()})
	previous := ""
	for i := 0; i < 5000; i++ {
		id := source.NewID("ing")
		if len(id) != len("ing_")+26 {
			t.Fatalf("unexpected id length %q", id)
		}
		if id[:4] != "ing_" {
			t.Fatalf("missing prefix in %q", id)
		}
		if id <= previous {
			t.Fatalf("ids not strictly increasing: %q then %q", previous, id)
		}
		previous = id
	}
}

func TestULIDSourceIncreasesAcrossTime(t *testing.T) {
	clock := &fixedClock{t: time.UnixMilli(1).UTC()}
	source := runtime.NewULIDSource(clock)
	first := source.NewID("job")
	clock.t = time.UnixMilli(2).UTC()
	second := source.NewID("job")
	if second <= first {
		t.Fatalf("later id %q not greater than %q", second, first)
	}
}

func TestRequestFingerprintStableAndSensitive(t *testing.T) {
	body := runtime.SHA256([]byte("raw event bytes"))
	headers := runtime.StableHeaders{
		EventID:     "evt_1",
		EventType:   "order.created",
		Route:       "orders.primary",
		ContentType: "application/json",
	}
	base := runtime.RequestFingerprint(body, headers)
	if base != runtime.RequestFingerprint(body, headers) {
		t.Fatal("fingerprint must be deterministic")
	}
	changed := headers
	changed.Route = "orders.secondary"
	if base == runtime.RequestFingerprint(body, changed) {
		t.Fatal("fingerprint must change when a stable header changes")
	}
	if base == runtime.RequestFingerprint(runtime.SHA256([]byte("other")), headers) {
		t.Fatal("fingerprint must change when the body hash changes")
	}
}

func TestParseHashRejectsNonCanonical(t *testing.T) {
	valid := runtime.SHA256([]byte("x")).String()
	if _, err := runtime.ParseHash(valid); err != nil {
		t.Fatalf("valid hash rejected: %v", err)
	}
	for _, bad := range []string{"", "ABC", valid[:63], valid[:63] + "G"} {
		if _, err := runtime.ParseHash(bad); err == nil {
			t.Fatalf("expected rejection for %q", bad)
		}
	}
}

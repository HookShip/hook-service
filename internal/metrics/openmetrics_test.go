package metrics

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteOpenMetricsRendersCountersAndGauges(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	registry.Add(CounterDeliveriesAttempted, 3)
	registry.Inc(CounterDeliveriesSucceeded)
	registry.SetGauge(GaugeInFlightDeliveries, 2)

	var buf bytes.Buffer
	if err := registry.WriteOpenMetrics(&buf); err != nil {
		t.Fatalf("WriteOpenMetrics() error = %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"# TYPE hookship_deliveries_attempted counter",
		"hookship_deliveries_attempted_total 3",
		"# TYPE hookship_deliveries_succeeded counter",
		"hookship_deliveries_succeeded_total 1",
		"# TYPE hookship_in_flight_deliveries gauge",
		"hookship_in_flight_deliveries 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}

	// Gauges must not carry the counter-only `_total` suffix.
	if strings.Contains(out, "hookship_in_flight_deliveries_total") {
		t.Errorf("gauge sample must not use the _total suffix\n%s", out)
	}
	// The stream must terminate with the mandatory OpenMetrics EOF marker.
	if !strings.HasSuffix(out, "# EOF\n") {
		t.Errorf("output must end with the OpenMetrics EOF marker\n%s", out)
	}
}

func TestWriteOpenMetricsEmptyRegistryIsJustEOF(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := NewRegistry().WriteOpenMetrics(&buf); err != nil {
		t.Fatalf("WriteOpenMetrics() error = %v", err)
	}
	if got := buf.String(); got != "# EOF\n" {
		t.Fatalf("empty registry output = %q, want %q", got, "# EOF\n")
	}
}

func TestWriteOpenMetricsIsPayloadFreeAndPrefixed(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	registry.Inc(CounterDeliveriesAttempted)

	var buf bytes.Buffer
	if err := registry.WriteOpenMetrics(&buf); err != nil {
		t.Fatalf("WriteOpenMetrics() error = %v", err)
	}
	// Every series name must be namespaced so a scraper can filter the service.
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "# EOF" || strings.HasPrefix(line, "# TYPE hookship_") {
			continue
		}
		if !strings.HasPrefix(line, "hookship_") {
			t.Fatalf("unexpected non-prefixed exposition line: %q", line)
		}
	}
}

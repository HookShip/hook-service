package metrics

import (
	"bufio"
	"io"
	"strconv"
)

// metricPrefix namespaces every exposed series so a scrape target can filter the
// service's metrics unambiguously.
const metricPrefix = "hookship_"

// WriteOpenMetrics renders the registry in the OpenMetrics text exposition
// format (a superset of the Prometheus text format) so a standard scraper can
// consume it. Counters are emitted with the required `_total` sample suffix and
// a `counter` TYPE; gauges use a `gauge` TYPE. Series are written in a stable,
// sorted order and the stream is terminated with the mandatory `# EOF` marker.
//
// The registry only ever holds payload-free counters and gauges, so this output
// never contains request bodies, payloads, secrets, or identifiers.
func (r *Registry) WriteOpenMetrics(w io.Writer) error {
	counters, gauges := r.Names()
	snapshot := r.Snapshot()

	buffered := bufio.NewWriter(w)
	for _, name := range counters {
		family := metricPrefix + name
		if _, err := io.WriteString(buffered, "# TYPE "+family+" counter\n"); err != nil {
			return err
		}
		if _, err := io.WriteString(buffered, family+"_total "+strconv.FormatUint(snapshot.Counters[name], 10)+"\n"); err != nil {
			return err
		}
	}
	for _, name := range gauges {
		family := metricPrefix + name
		if _, err := io.WriteString(buffered, "# TYPE "+family+" gauge\n"); err != nil {
			return err
		}
		if _, err := io.WriteString(buffered, family+" "+strconv.FormatInt(snapshot.Gauges[name], 10)+"\n"); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(buffered, "# EOF\n"); err != nil {
		return err
	}
	return buffered.Flush()
}

package rpproxy

import (
	"fmt"
	"strings"
	"sync/atomic"
)

type Metrics struct {
	ready            atomic.Bool
	active           atomic.Int64
	total            atomic.Uint64
	rejected         atomic.Uint64
	dialFailures     atomic.Uint64
	bytesToDevice    atomic.Uint64
	bytesToClient    atomic.Uint64
	packetsReflected atomic.Uint64
	bytesReflected   atomic.Uint64
}

type MetricsSnapshot struct {
	Ready            bool
	Active           int64
	Total            uint64
	Rejected         uint64
	DialFailures     uint64
	BytesToDevice    uint64
	BytesToClient    uint64
	PacketsReflected uint64
	BytesReflected   uint64
}

func (metrics *Metrics) SetReady(ready bool) {
	metrics.ready.Store(ready)
}

func (metrics *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		Ready:            metrics.ready.Load(),
		Active:           metrics.active.Load(),
		Total:            metrics.total.Load(),
		Rejected:         metrics.rejected.Load(),
		DialFailures:     metrics.dialFailures.Load(),
		BytesToDevice:    metrics.bytesToDevice.Load(),
		BytesToClient:    metrics.bytesToClient.Load(),
		PacketsReflected: metrics.packetsReflected.Load(),
		BytesReflected:   metrics.bytesReflected.Load(),
	}
}

func (metrics *Metrics) Prometheus() string {
	snapshot := metrics.Snapshot()
	ready := 0
	if snapshot.Ready {
		ready = 1
	}

	var output strings.Builder
	fmt.Fprintf(&output, "# TYPE rppairing_egress_ready gauge\n")
	fmt.Fprintf(&output, "rppairing_egress_ready %d\n", ready)
	fmt.Fprintf(&output, "# TYPE rppairing_egress_active_streams gauge\n")
	fmt.Fprintf(&output, "rppairing_egress_active_streams %d\n", snapshot.Active)
	fmt.Fprintf(&output, "# TYPE rppairing_egress_streams_total counter\n")
	fmt.Fprintf(&output, "rppairing_egress_streams_total %d\n", snapshot.Total)
	fmt.Fprintf(&output, "# TYPE rppairing_egress_rejected_total counter\n")
	fmt.Fprintf(&output, "rppairing_egress_rejected_total %d\n", snapshot.Rejected)
	fmt.Fprintf(&output, "# TYPE rppairing_egress_dial_failures_total counter\n")
	fmt.Fprintf(&output, "rppairing_egress_dial_failures_total %d\n", snapshot.DialFailures)
	fmt.Fprintf(&output, "# TYPE rppairing_egress_bytes_to_device_total counter\n")
	fmt.Fprintf(&output, "rppairing_egress_bytes_to_device_total %d\n", snapshot.BytesToDevice)
	fmt.Fprintf(&output, "# TYPE rppairing_egress_bytes_to_client_total counter\n")
	fmt.Fprintf(&output, "rppairing_egress_bytes_to_client_total %d\n", snapshot.BytesToClient)
	fmt.Fprintf(&output, "# TYPE rppairing_egress_packets_reflected_total counter\n")
	fmt.Fprintf(&output, "rppairing_egress_packets_reflected_total %d\n", snapshot.PacketsReflected)
	fmt.Fprintf(&output, "# TYPE rppairing_egress_packet_bytes_reflected_total counter\n")
	fmt.Fprintf(&output, "rppairing_egress_packet_bytes_reflected_total %d\n", snapshot.BytesReflected)
	return output.String()
}

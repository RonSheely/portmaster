package network

import (
	"context"
	"time"

	"github.com/safing/portbase/log"
	"github.com/safing/portmaster/network/packet"
	"github.com/safing/portmaster/network/state"
	"github.com/safing/portmaster/process"
)

const (
	// DeleteConnsAfterEndedThreshold defines the amount of time after which
	// ended connections should be removed from the internal connection state.
	DeleteConnsAfterEndedThreshold = 10 * time.Minute

	cleanerTickDuration = 5 * time.Second
)

func connectionCleaner(ctx context.Context) error {
	ticker := module.NewSleepyTicker(cleanerTickDuration, 0)

	for {
		select {
		case <-ctx.Done():
			ticker.Stop()
			return nil
		case <-ticker.Wait():
			// clean connections and processes
			activePIDs := cleanConnections()
			process.CleanProcessStorage(activePIDs)

			// clean udp connection states
			state.CleanUDPStates(ctx)
		}
	}
}

func cleanConnections() (activePIDs map[int]struct{}) {
	activePIDs = make(map[int]struct{})

	_ = module.RunMicroTask("clean connections", 0, func(ctx context.Context) error {
		now := time.Now().UTC()
		nowUnix := now.Unix()
		deleteOlderThan := now.Add(-DeleteConnsAfterEndedThreshold).Unix()

		// network connections
		for _, conn := range conns.clone() {
			conn.Lock()

			// delete inactive connections
			switch {
			case !conn.DataIsComplete():
				// Step 0: delete old incomplete connections
				if conn.Started < deleteOlderThan {
					conn.delete()
				}
			case conn.Ended == 0:
				// Step 1: check if still active
				exists := state.Exists(&packet.Info{
					Inbound:  false, // src == local
					Version:  conn.IPVersion,
					Protocol: conn.IPProtocol,
					Src:      conn.LocalIP,
					SrcPort:  conn.LocalPort,
					Dst:      conn.Entity.IP,
					DstPort:  conn.Entity.Port,
				}, now)

				activePIDs[conn.process.Pid] = struct{}{}

				if !exists {
					// Step 2: mark end
					conn.Ended = nowUnix
					conn.Save()
				}
			case conn.Ended < deleteOlderThan:
				// Step 3: delete
				log.Tracef("network.clean: deleted %s (ended at %s)", conn.DatabaseKey(), time.Unix(conn.Ended, 0))
				conn.delete()
			}

			conn.Unlock()
		}

		// dns requests
		for _, conn := range dnsConns.clone() {
			conn.Lock()

			// delete old dns connections
			if conn.Ended < deleteOlderThan {
				log.Tracef("network.clean: deleted %s (ended at %s)", conn.DatabaseKey(), time.Unix(conn.Ended, 0))
				conn.delete()
			}

			conn.Unlock()
		}

		return nil
	})

	return activePIDs
}

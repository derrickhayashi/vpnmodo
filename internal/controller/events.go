package controller

import (
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
	"time"
)

// This reads only local sanitized files, including inactive-profile spools.
// It never probes a gateway, restarts a container, or initiates authentication.
func (s *Server) maintainLogs() {
	defer s.workers.Done()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}
		if collector, ok := s.config.Adapter.(interface{ CollectDiagnostics(profile.Profile) }); ok {
			profiles, err := s.profiles.List(s.ctx)
			if err == nil {
				for _, p := range profiles {
					if s.ctx.Err() != nil {
						return
					}
					_ = s.profiles.WithProfile(s.ctx, p.Name, func(snap profile.Snapshot) error { collector.CollectDiagnostics(snap.Profile); return nil })
				}
			}
		}
		_ = s.config.events.Maintain()
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

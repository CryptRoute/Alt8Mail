package control

import (
	"context"
	"strconv"
	"time"
)

// Start launches the long-running monitors and workers. The control service
// is the infrastructure observer, so these jobs are intentionally independent
// from any customer request.
func (s *Server) Start(ctx context.Context) {
	logIngestor := newLogIngestor(s)
	logIngestor.Start(ctx)
	s.StartImportWorker(ctx)
	s.StartPolicyDaemon(ctx)
	if changed, err := s.syncPolicyServiceConfig(ctx); err != nil {
		s.logger.Warn("policy service configuration failed", "err", err)
	} else if changed {
		if err := reloadPostfix(ctx); err != nil {
			s.logger.Warn("postfix reload after policy configuration failed", "err", err)
		}
	}

	go s.runServiceMonitor(ctx)
	go s.runHostBroadcast(ctx)
	go s.runBlocklistScheduler(ctx)
	go s.runCertificateMonitor(ctx)
	go s.runPruneScheduler(ctx)
	go s.runPackageMonitor(ctx)
	go s.runServiceHealthHeartbeat(ctx)
}

func (s *Server) runServiceMonitor(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.HealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			services, err := s.readServicesWithContext(ctx)
			if err != nil {
				s.logger.Warn("service monitor failed", "err", err)
				continue
			}
			s.persistServiceStates(ctx, services)
		}
	}
}

func (s *Server) runServiceHealthHeartbeat(ctx context.Context) {
	// Prime the state table once so the dashboard has a baseline before the
	// first polling interval.
	services, err := s.readServicesWithContext(ctx)
	if err == nil {
		s.persistServiceStates(ctx, services)
	}
	<-ctx.Done()
}

func (s *Server) readServicesWithContext(ctx context.Context) ([]serviceStatus, error) {
	return s.probe.serviceStatuses(ctx, true), nil
}

func (s *Server) persistServiceStates(ctx context.Context, services []serviceStatus) {
	for _, service := range services {
		var previous bool
		var exists bool
		err := s.db.QueryRow(ctx, `SELECT active FROM control.service_state WHERE unit=$1`, service.Unit).Scan(&previous)
		if err == nil {
			exists = true
		}
		if !exists {
			_, _ = s.db.Exec(ctx, `
				INSERT INTO control.service_state (unit,active,previous_active,state,sub_state,enabled_state,last_checked_at,last_change_at,detail)
				VALUES ($1,$2,NULL,$3,$4,$5,now(),now(),$6)
				ON CONFLICT (unit) DO UPDATE SET active=EXCLUDED.active,state=EXCLUDED.state,
				  sub_state=EXCLUDED.sub_state,enabled_state=EXCLUDED.enabled_state,last_checked_at=now()
			`, service.Unit, service.Active, service.ActiveState, service.SubState, service.EnabledState, service.Description)
			continue
		}
		if previous == service.Active {
			_, _ = s.db.Exec(ctx, `UPDATE control.service_state SET state=$2,sub_state=$3,enabled_state=$4,last_checked_at=now() WHERE unit=$1`,
				service.Unit, service.ActiveState, service.SubState, service.EnabledState)
			continue
		}
		_, _ = s.db.Exec(ctx, `
			UPDATE control.service_state SET active=$2,previous_active=$3,state=$4,sub_state=$5,
			  enabled_state=$6,last_checked_at=now(),last_change_at=now() WHERE unit=$1
		`, service.Unit, service.Active, previous, service.ActiveState, service.SubState, service.EnabledState)
		event := "service.down"
		if service.Active {
			event = "service.up"
		}
		s.hub.Broadcast("service.transition", map[string]any{"unit": service.Unit, "event": event, "state": service.SubState})
		s.notifyEvent(ctx, event, map[string]any{"unit": service.Unit, "name": service.Name, "state": service.ActiveState, "subState": service.SubState})
	}
}

func (s *Server) runHostBroadcast(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.MetricsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			metrics := s.probe.hostMetrics(ctx, false)
			s.hub.Broadcast("host.metrics", map[string]any{"host": metrics})
		}
	}
}

func (s *Server) runBlocklistScheduler(ctx context.Context) {
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	if _, err := s.runBlocklistCheck(ctx); err != nil {
		s.logger.Info("blocklist check skipped", "reason", err)
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.runBlocklistCheck(ctx); err != nil {
				s.logger.Warn("blocklist check failed", "err", err)
			}
		}
	}
}

func (s *Server) runCertificateMonitor(ctx context.Context) {
	check := func() {
		certs, err := s.scanCertificates(ctx)
		if err != nil {
			s.logger.Warn("certificate scan failed", "err", err)
			return
		}
		warnDays := s.settingInt(ctx, "ssl.warn_days", 14)
		for _, cert := range certs {
			if cert.DaysLeft > warnDays {
				continue
			}
			key := "ssl.warned." + cert.Name
			last := s.settingString(ctx, key, "")
			if last == cert.NotAfter {
				continue
			}
			_ = s.setSetting(ctx, key, cert.NotAfter, "")
			s.notifyEvent(ctx, "ssl.expiring", map[string]any{
				"certificate": cert.Name, "subject": cert.Subject, "notAfter": cert.NotAfter,
				"daysLeft": cert.DaysLeft, "thresholdDays": warnDays,
			})
			_, _ = s.db.Exec(ctx, `UPDATE control.ssl_certificates SET last_error=$2 WHERE name=$1`, cert.Name, "expires in "+itoaDays(cert.DaysLeft))
		}
	}
	check()
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func itoaDays(days int) string {
	if days < 0 {
		return "expired"
	}
	if days == 1 {
		return "1 day"
	}
	return strconv.Itoa(days) + " days"
}

func (s *Server) runPruneScheduler(ctx context.Context) {
	s.pruneEvents(ctx)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pruneEvents(ctx)
		}
	}
}

func (s *Server) runPackageMonitor(ctx context.Context) {
	s.probe.refreshPackageUpdates(ctx, true)
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.probe.refreshPackageUpdates(ctx, true)
		}
	}
}

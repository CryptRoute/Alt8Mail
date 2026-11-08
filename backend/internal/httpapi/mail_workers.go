package httpapi

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// userMailLock serializes all IMAP work for one mailbox. A user's full sync,
// new-mail poll, retention pass and web action must not interleave, but work for
// different users is independent and may run in parallel.
func (s *Server) userMailLock(userID string) *sync.Mutex {
	s.mailUserMu.Lock()
	defer s.mailUserMu.Unlock()
	lock := s.mailLocks[userID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.mailLocks[userID] = lock
	}
	return lock
}

type mailWorker struct {
	accountID string
	kind      string
	email     string
	user      userRecord
	account   mailAccountRecord
	cancel    context.CancelFunc
	done      chan struct{}
}

type mailWorkerAccount struct {
	account mailAccountRecord
	user    userRecord
}

type mailWorkerPool struct {
	server  *Server
	ctx     context.Context
	mu      sync.Mutex
	workers map[string]*mailWorker
	sem     chan struct{}
}

func (s *Server) startMailWorkerPool(ctx context.Context, concurrency int) *mailWorkerPool {
	if concurrency < 1 {
		concurrency = 1
	}
	pool := &mailWorkerPool{
		server: s, ctx: ctx, workers: map[string]*mailWorker{}, sem: make(chan struct{}, concurrency),
	}
	go pool.run()
	return pool
}

func (p *mailWorkerPool) run() {
	p.refresh()
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			p.stopAll()
			return
		case <-ticker.C:
			p.refresh()
		}
	}
}

func (p *mailWorkerPool) refresh() {
	accounts, err := p.server.activeMailWorkerAccounts(p.ctx)
	if err != nil {
		p.server.logger.Warn("mail worker account refresh failed", "err", err)
		return
	}
	active := map[string]mailWorkerAccount{}
	policies := map[string]adminTenantPolicy{}
	for _, item := range accounts {
		policy, ok := policies[item.user.TenantID]
		if !ok {
			policy = p.server.tenantPolicyFor(p.ctx, item.user.TenantID)
			policies[item.user.TenantID] = policy
		}
		if !policy.Mail.IMAPAccess {
			continue
		}
		if item.account.Kind == mailAccountKindNative {
			if !p.server.mailAccountConfigured(item.user.Email) {
				continue
			}
		} else if !policy.Mail.ExternalAccounts {
			continue
		}
		active[item.account.ID] = item
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for id, worker := range p.workers {
		item, wanted := active[id]
		if wanted && item.user.ID == worker.user.ID && item.user.Email == worker.email {
			delete(active, id)
			continue
		}
		worker.cancel()
		delete(p.workers, id)
	}
	for id, item := range active {
		workerCtx, cancel := context.WithCancel(p.ctx)
		worker := &mailWorker{
			accountID: id, kind: item.account.Kind, email: item.user.Email,
			user: item.user, account: item.account, cancel: cancel, done: make(chan struct{}),
		}
		p.workers[id] = worker
		go p.runWorker(workerCtx, worker)
	}
}

func (s *Server) activeMailWorkerAccounts(ctx context.Context) ([]mailWorkerAccount, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT a.id, a.tenant_id::text, a.user_id::text, a.kind, a.provider, a.email::text,
		       a.display_name, a.color, a.status, a.auth_type, a.imap_host, a.imap_port, a.imap_tls,
		       a.smtp_host, a.smtp_port, a.smtp_tls, a.username, a.settings, a.last_sync_at,
		       a.last_error, a.created_at, a.updated_at,
		       u.email::text, u.name, u.initials, u.color, u.role, u.status, u.preferences, u.security,
		       u.quota_bytes, u.used_bytes, u.enrollment_required, u.force_password_change, u.password_expires_at
		FROM public.mail_accounts a
		JOIN public.users u ON u.id = a.user_id
		JOIN public.tenants t ON t.id = a.tenant_id
		WHERE a.status = 'active' AND u.status = 'active' AND u.deleted_at IS NULL AND t.status = 'active'
		  AND (a.kind <> 'native' OR EXISTS (
			SELECT 1 FROM public.mailboxes m WHERE m.user_id = a.user_id AND m.active
		  ))
		ORDER BY a.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []mailWorkerAccount{}
	for rows.Next() {
		var item mailWorkerAccount
		var settings []byte
		if err := rows.Scan(
			&item.account.ID, &item.account.TenantID, &item.account.UserID, &item.account.Kind,
			&item.account.Provider, &item.account.Email, &item.account.DisplayName, &item.account.Color,
			&item.account.Status, &item.account.AuthType, &item.account.IMAPHost, &item.account.IMAPPort,
			&item.account.IMAPTLS, &item.account.SMTPHost, &item.account.SMTPPort, &item.account.SMTPTLS,
			&item.account.Username, &settings, &item.account.LastSyncAt, &item.account.LastError,
			&item.account.CreatedAt, &item.account.UpdatedAt,
			&item.user.Email, &item.user.Name, &item.user.Initials, &item.user.Color, &item.user.Role,
			&item.user.Status, &item.user.Preferences, &item.user.Security, &item.user.QuotaBytes,
			&item.user.UsedBytes, &item.user.EnrollmentRequired, &item.user.ForcePasswordChange,
			&item.user.PasswordExpiresAt,
		); err != nil {
			return nil, err
		}
		item.user.ID = item.account.UserID
		item.user.TenantID = item.account.TenantID
		if len(settings) > 0 {
			item.account.Settings = settings
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (p *mailWorkerPool) stopAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, worker := range p.workers {
		worker.cancel()
		delete(p.workers, id)
	}
}

func (p *mailWorkerPool) runWorker(ctx context.Context, worker *mailWorker) {
	defer close(worker.done)
	if worker.kind == mailAccountKindExternal {
		p.runExternalWorker(ctx, worker)
		return
	}
	user := worker.user
	poll := p.server.cfg.IMAPPollInterval
	full := p.server.cfg.IMAPSyncInterval
	if poll <= 0 {
		poll = 15 * time.Second
	}
	if full <= 0 {
		full = 5 * time.Minute
	}
	// Spread the initial INBOX checks across a poll window so a restart with a
	// large user base does not open every mailbox at the same instant. Using
	// UnixNano modulo the interval gives every worker created in the same
	// refresh nearly the same delay, so use the process-wide random source.
	jitter := time.Duration(rand.Int63n(int64(poll)))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}
	p.runCheck(ctx, user)
	if p.server.cfg.IMAPIdle && p.server.masterMailConfigured() {
		go p.runIdle(ctx, user)
	}

	pollTicker := time.NewTicker(poll)
	defer pollTicker.Stop()
	fullTicker := time.NewTicker(full)
	defer fullTicker.Stop()
	// A worker that starts after a restart needs one complete folder scan soon,
	// but the scan must not fire for every mailbox at once.
	initialFullDelay := minDuration(2*time.Minute, maxDuration(20*time.Second, full/2))
	initialFullDelay += time.Duration(rand.Int63n(int64(30 * time.Second)))
	initialFull := time.NewTimer(initialFullDelay)
	defer initialFull.Stop()
	syncFailures := 0
	var nextFullAt time.Time
	runFull := func() {
		if !nextFullAt.IsZero() && time.Now().Before(nextFullAt) {
			return
		}
		if err := p.runFullSync(ctx, user); err != nil {
			syncFailures++
			delay := fullSyncRetryDelay(full, syncFailures)
			nextFullAt = time.Now().Add(delay)
			p.server.logger.Warn("mail worker full sync failed", "user", user.Email, "err", err,
				"failures", syncFailures, "retryIn", delay.String())
			return
		}
		syncFailures = 0
		nextFullAt = time.Time{}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-pollTicker.C:
			p.runCheck(ctx, user)
		case <-initialFull.C:
			runFull()
		case <-fullTicker.C:
			runFull()
		}
	}
}

func (p *mailWorkerPool) runExternalWorker(ctx context.Context, worker *mailWorker) {
	jitter := 5*time.Second + time.Duration(rand.Int63n(int64(25*time.Second)))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}
	p.syncExternalWorker(ctx, worker)
	pollTicker := time.NewTicker(60 * time.Second)
	defer pollTicker.Stop()
	fullTicker := time.NewTicker(10 * time.Minute)
	defer fullTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pollTicker.C:
			p.syncExternalWorker(ctx, worker)
		case <-fullTicker.C:
			p.syncExternalWorker(ctx, worker)
		}
	}
}

func (p *mailWorkerPool) syncExternalWorker(ctx context.Context, worker *mailWorker) {
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return
	}
	if err := p.server.syncExternalAccount(ctx, worker.account, worker.user); err != nil {
		p.server.logger.Debug("connected account sync failed", "account", worker.account.Email, "err", err)
	}
}

func (p *mailWorkerPool) runCheck(ctx context.Context, user userRecord) {
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return
	}
	imported, err := p.server.checkNewInboxForUser(ctx, user)
	if err != nil {
		p.server.logger.Debug("mail worker inbox check failed", "user", user.Email, "err", err)
		return
	}
	if imported > 0 {
		p.server.logger.Info("new mail imported", "user", user.Email, "messages", imported)
		p.server.publishRealtimeTo(user.ID, "mail")
	}
}

func (p *mailWorkerPool) runIdle(ctx context.Context, user userRecord) {
	backoff := idleReconnectDelay
	for {
		if ctx.Err() != nil {
			return
		}
		active, event, err := p.server.idleINBOXOnce(ctx, user)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.server.logger.Info("IMAP IDLE connection failed", "user", user.Email, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < time.Minute {
				backoff *= 2
			}
			continue
		}
		if !active {
			// The server does not advertise IDLE; the regular poll worker is
			// the fallback and this goroutine exits.
			return
		}
		backoff = idleReconnectDelay
		p.runCheck(ctx, user)
		// New mail was already imported with its current flags. A full INBOX
		// flag reconcile on every arrival would fetch flags for the whole
		// mailbox, so only expunge/FETCH events trigger it.
		if event != idleEventNewMail {
			if err := p.server.refreshInboxFlagsForUser(ctx, user); err != nil {
				p.server.logger.Debug("IMAP IDLE flag refresh failed", "user", user.Email, "err", err)
			}
		}
	}
}

func (p *mailWorkerPool) runFullSync(ctx context.Context, user userRecord) error {
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	result, err := p.server.syncUserMailbox(ctx, user)
	if err == nil {
		if imported, ok := result["imported"].(int); ok && imported > 0 {
			p.server.publishRealtimeTo(user.ID, "mail")
		}
	}
	return err
}

// fullSyncRetryDelay backs off repeated full-sync failures so an undiagnosed
// poison message cannot hammer IMAP and the database every five minutes. It
// caps at one hour and resets after a successful scan.
func fullSyncRetryDelay(base time.Duration, failures int) time.Duration {
	if base <= 0 {
		base = 5 * time.Minute
	}
	if failures < 1 {
		return base
	}
	delay := base
	for i := 1; i < failures && delay < time.Hour; i++ {
		delay *= 2
		if delay > time.Hour {
			delay = time.Hour
		}
	}
	return delay
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

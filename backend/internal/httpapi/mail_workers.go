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
	userID string
	email  string
	cancel context.CancelFunc
	done   chan struct{}
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
	users, err := p.server.activeMailUsers(p.ctx)
	if err != nil {
		p.server.logger.Warn("mail worker user refresh failed", "err", err)
		return
	}
	active := map[string]userRecord{}
	policies := map[string]adminTenantPolicy{}
	for _, user := range users {
		policy, ok := policies[user.TenantID]
		if !ok {
			policy = p.server.tenantPolicyFor(p.ctx, user.TenantID)
			policies[user.TenantID] = policy
		}
		if !policy.Mail.IMAPAccess || !p.server.mailAccountConfigured(user.Email) {
			continue
		}
		active[user.ID] = user
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for id, worker := range p.workers {
		user, wanted := active[id]
		if wanted && user.Email == worker.email {
			delete(active, id)
			continue
		}
		worker.cancel()
		delete(p.workers, id)
	}
	for id, user := range active {
		workerCtx, cancel := context.WithCancel(p.ctx)
		worker := &mailWorker{userID: id, email: user.Email, cancel: cancel, done: make(chan struct{})}
		p.workers[id] = worker
		go p.runWorker(workerCtx, worker, user)
	}
}

func (p *mailWorkerPool) stopAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, worker := range p.workers {
		worker.cancel()
		delete(p.workers, id)
	}
}

func (p *mailWorkerPool) runWorker(ctx context.Context, worker *mailWorker, user userRecord) {
	defer close(worker.done)
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
	for {
		select {
		case <-ctx.Done():
			return
		case <-pollTicker.C:
			p.runCheck(ctx, user)
		case <-initialFull.C:
			p.runFullSync(ctx, user)
		case <-fullTicker.C:
			p.runFullSync(ctx, user)
		}
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

func (p *mailWorkerPool) runFullSync(ctx context.Context, user userRecord) {
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return
	}
	if _, err := p.server.syncUserMailbox(ctx, user); err != nil {
		p.server.logger.Warn("mail worker full sync failed", "user", user.Email, "err", err)
	}
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

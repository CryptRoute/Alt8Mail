package httpapi

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// taskNextOccurrence returns the next due date for a recurring task. The
// supported vocabulary is the same common RRULE subset used by calendar
// feeds: daily, weekly (with BYDAY) and monthly, with INTERVAL, COUNT and
// UNTIL. The returned date is local-midnight safe because tasks store dates,
// not instants.
func taskNextOccurrence(ruleRaw string, base time.Time, now time.Time, generated int) (time.Time, bool) {
	rule, ok := parseICSRecurrenceRule(ruleRaw)
	if !ok {
		return time.Time{}, false
	}
	if rule.Count > 0 && generated >= rule.Count {
		return time.Time{}, false
	}
	base = time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, time.UTC)
	nowDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	next := base
	advance := func() bool {
		switch rule.Freq {
		case "DAILY":
			next = next.AddDate(0, 0, rule.Interval)
		case "MONTHLY":
			next = next.AddDate(0, rule.Interval, 0)
		case "WEEKLY":
			allowed := map[time.Weekday]bool{}
			for _, day := range rule.ByDay {
				allowed[day] = true
			}
			if len(allowed) == 0 {
				allowed[base.Weekday()] = true
			}
			weekStart := next.AddDate(0, 0, -int(next.Weekday()))
			for offset := 1; offset <= 7*rule.Interval; offset++ {
				candidate := next.AddDate(0, 0, offset)
				if !allowed[candidate.Weekday()] {
					continue
				}
				if rule.Interval > 1 {
					weeks := (dayNumberUTC(candidate) - dayNumberUTC(weekStart)) / 7
					if weeks%rule.Interval != 0 {
						continue
					}
				}
				next = candidate
				return true
			}
			return false
		}
		return true
	}
	for attempts := 0; attempts < 1000; attempts++ {
		if !advance() {
			return time.Time{}, false
		}
		if !rule.Until.IsZero() && next.After(rule.Until) {
			return time.Time{}, false
		}
		if next.After(nowDay) || next.Equal(nowDay) {
			return next, true
		}
	}
	return time.Time{}, false
}

// createNextRecurringTask inserts the next occurrence after a task is marked
// complete. The partial unique index on parent_task_id makes this idempotent
// even when an update is retried.
func (s *Server) createNextRecurringTask(ctx context.Context, sess *sessionRecord, taskID string) error {
	var title, notes, priority, assignee, threadID, subject, source, kanban, rule string
	var tags []string
	var due *time.Time
	var reminder *time.Time
	err := s.store.DB.QueryRow(ctx, `
		SELECT title, notes, priority, COALESCE(assignee_user_id::text,''),
		       COALESCE(linked_thread_id,''), COALESCE(linked_subject,''), source, tags,
		       kanban_status, recurrence_rule, due_date::timestamptz, reminder_at
		FROM public.tasks
		WHERE id=$1 AND user_id=$2 AND status='done' AND recurrence_rule <> ''
	`, taskID, sess.User.ID).Scan(&title, &notes, &priority, &assignee, &threadID, &subject, &source, &tags,
		&kanban, &rule, &due, &reminder)
	if err != nil {
		return nil
	}
	base := time.Now().UTC()
	dueTime := "09:00"
	if due != nil {
		base = due.UTC()
	}
	var dueClock string
	if due != nil {
		_ = s.store.DB.QueryRow(ctx, `SELECT COALESCE(to_char(due_time,'HH24:MI'),'09:00') FROM public.tasks WHERE id=$1`, taskID).Scan(&dueClock)
		if strings.TrimSpace(dueClock) == "" {
			dueClock = dueTime
		}
	}
	var generated int
	_ = s.store.DB.QueryRow(ctx, `
		WITH RECURSIVE chain AS (
		  SELECT id, parent_task_id FROM public.tasks WHERE id=$1
		  UNION ALL
		  SELECT t.id, t.parent_task_id FROM public.tasks t JOIN chain c ON t.parent_task_id=c.id
		)
		SELECT count(*) FROM chain
	`, taskID).Scan(&generated)
	next, ok := taskNextOccurrence(rule, base, time.Now().UTC(), generated)
	if !ok {
		return nil
	}
	var nextReminder *time.Time
	if reminder != nil {
		shift := reminder.Sub(base)
		value := next.Add(shift)
		nextReminder = &value
	}
	_, err = s.store.DB.Exec(ctx, `
		INSERT INTO public.tasks
		  (id, tenant_id, user_id, title, notes, status, priority, due_date, due_time,
		   assignee_user_id, linked_thread_id, linked_subject, source, tags, kanban_status,
		   recurrence_rule, reminder_at, parent_task_id)
		SELECT $1,$2,$3,$4,$5,'open',$6,$7::date,$8::time,
		       NULLIF($9,'')::uuid, NULLIF($10,''), NULLIF($11,''),'recurrence',$12,$13,
		       $14,$15,$16
		WHERE NOT EXISTS (SELECT 1 FROM public.tasks WHERE parent_task_id=$16)
	`, newID("task"), sess.User.TenantID, sess.User.ID, title, notes, priority,
		next.Format("2006-01-02"), dueClock, assignee, threadID, subject, tags, kanban,
		rule, nextReminder, taskID)
	if err != nil {
		return fmt.Errorf("create recurring task: %w", err)
	}
	s.recordWorkspaceAudit(ctx, sess, "task.recurred", title, "Next occurrence created for "+next.Format("2006-01-02"))
	return nil
}

func (s *Server) taskReminderWorker(ctx context.Context) {
	s.runTaskReminders(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runTaskReminders(ctx)
		}
	}
}

func (s *Server) runTaskReminders(ctx context.Context) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, tenant_id::text, user_id::text, title, notes, reminder_at
		FROM public.tasks
		WHERE status='open' AND reminder_at IS NOT NULL AND reminder_at <= now()
		  AND reminder_sent_at IS NULL
		ORDER BY reminder_at ASC LIMIT 100
	`)
	if err != nil {
		return
	}
	type reminderRow struct {
		id, tenant, user, title, notes string
		at                             time.Time
	}
	pending := []reminderRow{}
	for rows.Next() {
		var item reminderRow
		if rows.Scan(&item.id, &item.tenant, &item.user, &item.title, &item.notes, &item.at) == nil {
			pending = append(pending, item)
		}
	}
	rows.Close()
	for _, item := range pending {
		var claimed string
		err := s.store.DB.QueryRow(ctx, `
			UPDATE public.tasks SET reminder_sent_at=now(), updated_at=now()
			WHERE id=$1 AND reminder_sent_at IS NULL
			RETURNING id
		`, item.id).Scan(&claimed)
		if err != nil || claimed == "" {
			continue
		}
		_, _ = s.store.DB.Exec(ctx, `
			INSERT INTO public.mail_notifications (id, tenant_id, user_id, kind, title, body, detail)
			VALUES ($1,$2,$3,'task',$4,$5,$6)
		`, newID("tasknotif"), item.tenant, item.user,
			truncate("Task reminder: "+item.title, 240),
			truncate(item.notes, 2000),
			"Due "+item.at.UTC().Format(time.RFC3339))
	}
}

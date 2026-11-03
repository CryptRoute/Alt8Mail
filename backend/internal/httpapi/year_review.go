package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) registerYearReviewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/yearly-review", s.requireUser(s.yearlyReview))
}

func userTimeLocation(user userRecord) *time.Location {
	prefs := preferencesFor(user)
	zone := strings.TrimSpace(fmt.Sprint(prefs["timeZone"]))
	if zone == "" {
		return time.UTC
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return time.UTC
	}
	return location
}

func (s *Server) yearlyReview(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	location := userTimeLocation(sess.User)
	now := time.Now().In(location)
	year, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("year")))
	if year < 2000 || year > now.Year()+1 {
		year = now.Year()
	}
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, location)
	end := start.AddDate(1, 0, 0)
	ctx := r.Context()
	zoneName := location.String()

	years := []int{}
	if rows, err := s.store.DB.Query(ctx, `
		SELECT DISTINCT extract(year from sent_at AT TIME ZONE $2)::int
		FROM public.mail_messages WHERE user_id=$1
		ORDER BY 1 DESC
	`, sess.User.ID, zoneName); err == nil {
		for rows.Next() {
			var value int
			if rows.Scan(&value) == nil && value >= 2000 {
				years = append(years, value)
			}
		}
		rows.Close()
	}
	if len(years) == 0 {
		years = []int{year}
	}
	if !containsInt(years, year) {
		years = append([]int{year}, years...)
	}

	var total, sent, received, threads, unread, newsletters int
	var chars int64
	_ = s.store.DB.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE folder='sent'),
		       count(*) FILTER (WHERE folder<>'sent'),
		       count(DISTINCT thread_id),
		       count(*) FILTER (WHERE unread),
		       count(*) FILTER (WHERE unsubscribe IS NOT NULL AND unsubscribe <> '{}'::jsonb),
		       COALESCE(sum(length(body_text)), 0)
		FROM public.mail_messages
		WHERE user_id=$1 AND sent_at >= $2 AND sent_at < $3 AND NOT draft
	`, sess.User.ID, start.UTC(), end.UTC()).Scan(&total, &sent, &received, &threads, &unread, &newsletters, &chars)
	readingMinutes := int(chars / 6 / 220)
	if chars > 0 && readingMinutes < 1 {
		readingMinutes = 1
	}

	type contactCount struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Count int    `json:"count"`
	}
	topSenders := []contactCount{}
	if rows, err := s.store.DB.Query(ctx, `
		SELECT COALESCE(NULLIF(from_name,''), from_email::text), lower(from_email::text), count(*)
		FROM public.mail_messages
		WHERE user_id=$1 AND sent_at >= $2 AND sent_at < $3 AND folder<>'sent' AND NOT draft
		  AND lower(from_email::text) <> lower($4)
		GROUP BY 1,2 ORDER BY count(*) DESC, 1 ASC LIMIT 5
	`, sess.User.ID, start.UTC(), end.UTC(), sess.User.Email); err == nil {
		for rows.Next() {
			var item contactCount
			if rows.Scan(&item.Name, &item.Email, &item.Count) == nil {
				topSenders = append(topSenders, item)
			}
		}
		rows.Close()
	}
	topRecipients := []contactCount{}
	if rows, err := s.store.DB.Query(ctx, `
		SELECT COALESCE(NULLIF(p->>'name',''), p->>'email'), lower(p->>'email'), count(*)
		FROM public.mail_messages m,
		     jsonb_array_elements(CASE WHEN jsonb_typeof(m.to_json)='array' THEN m.to_json ELSE '[]'::jsonb END) p
		WHERE m.user_id=$1 AND m.sent_at >= $2 AND m.sent_at < $3 AND m.folder='sent' AND NOT m.draft
		  AND lower(p->>'email') <> lower($4)
		GROUP BY 1,2 ORDER BY count(*) DESC, 1 ASC LIMIT 5
	`, sess.User.ID, start.UTC(), end.UTC(), sess.User.Email); err == nil {
		for rows.Next() {
			var item contactCount
			if rows.Scan(&item.Name, &item.Email, &item.Count) == nil {
				topRecipients = append(topRecipients, item)
			}
		}
		rows.Close()
	}

	monthly := make([]int, 12)
	if rows, err := s.store.DB.Query(ctx, `
		SELECT extract(month from sent_at AT TIME ZONE $4)::int, count(*)
		FROM public.mail_messages
		WHERE user_id=$1 AND sent_at >= $2 AND sent_at < $3 AND NOT draft
		GROUP BY 1
	`, sess.User.ID, start.UTC(), end.UTC(), zoneName); err == nil {
		for rows.Next() {
			var month, count int
			if rows.Scan(&month, &count) == nil && month >= 1 && month <= 12 {
				monthly[month-1] = count
			}
		}
		rows.Close()
	}
	busiestMonth, busiestMonthCount := 0, 0
	monthlyItems := make([]map[string]any, 0, 12)
	for index, count := range monthly {
		label := time.Month(index + 1).String()
		monthlyItems = append(monthlyItems, map[string]any{"month": index + 1, "label": label, "count": count})
		if count > busiestMonthCount {
			busiestMonth, busiestMonthCount = index+1, count
		}
	}
	var busiestDay string
	var busiestDayCount int
	_ = s.store.DB.QueryRow(ctx, `
		SELECT to_char(sent_at AT TIME ZONE $4, 'YYYY-MM-DD'), count(*)
		FROM public.mail_messages
		WHERE user_id=$1 AND sent_at >= $2 AND sent_at < $3 AND NOT draft
		GROUP BY 1 ORDER BY count(*) DESC, 1 ASC LIMIT 1
	`, sess.User.ID, start.UTC(), end.UTC(), zoneName).Scan(&busiestDay, &busiestDayCount)
	var busiestHour, busiestHourCount int
	_ = s.store.DB.QueryRow(ctx, `
		SELECT extract(hour from sent_at AT TIME ZONE $4)::int, count(*)
		FROM public.mail_messages
		WHERE user_id=$1 AND sent_at >= $2 AND sent_at < $3 AND NOT draft
		GROUP BY 1 ORDER BY count(*) DESC, 1 ASC LIMIT 1
	`, sess.User.ID, start.UTC(), end.UTC(), zoneName).Scan(&busiestHour, &busiestHourCount)
	var reactionsGiven, reactionsReceived int
	_ = s.store.DB.QueryRow(ctx, `
		SELECT count(*) FROM public.mail_reactions
		WHERE user_id=$1 AND dismissed_at IS NULL AND created_at >= $2 AND created_at < $3
	`, sess.User.ID, start.UTC(), end.UTC()).Scan(&reactionsGiven)
	_ = s.store.DB.QueryRow(ctx, `
		SELECT count(*) FROM public.mail_reactions
		WHERE owner_user_id=$1 AND user_id<>$1 AND visibility='sent' AND dismissed_at IS NULL
		  AND created_at >= $2 AND created_at < $3
	`, sess.User.ID, start.UTC(), end.UTC()).Scan(&reactionsReceived)
	topReactions := []map[string]any{}
	if rows, err := s.store.DB.Query(ctx, `
		SELECT emoji, count(*) FROM public.mail_reactions
		WHERE user_id=$1 AND dismissed_at IS NULL AND created_at >= $2 AND created_at < $3
		GROUP BY 1 ORDER BY count(*) DESC, 1 ASC LIMIT 5
	`, sess.User.ID, start.UTC(), end.UTC()); err == nil {
		for rows.Next() {
			var emoji string
			var count int
			if rows.Scan(&emoji, &count) == nil {
				topReactions = append(topReactions, map[string]any{"emoji": emoji, "count": count, "semantic": reactionEmojiSemantics[emoji]})
			}
		}
		rows.Close()
	}
	var unsubscribed int
	_ = s.store.DB.QueryRow(ctx, `
		SELECT count(*) FROM public.mail_unsubscribe_actions
		WHERE user_id=$1 AND created_at >= $2 AND created_at < $3
	`, sess.User.ID, start.UTC(), end.UTC()).Scan(&unsubscribed)

	highlights := []string{}
	if total == 0 {
		highlights = append(highlights, "No mail in this year yet — your story is still being written.")
	} else {
		highlights = append(highlights, fmt.Sprintf("You handled %s messages across %d conversations.", formatCount(total), threads))
		highlights = append(highlights, fmt.Sprintf("You sent %s and received %s.", formatCount(sent), formatCount(received)))
		if busiestMonth > 0 {
			highlights = append(highlights, fmt.Sprintf("%s was your busiest month with %s messages.", time.Month(busiestMonth).String(), formatCount(busiestMonthCount)))
		}
		if len(topSenders) > 0 {
			highlights = append(highlights, fmt.Sprintf("%s was your most frequent correspondent.", topSenders[0].Name))
		}
		if readingMinutes > 0 {
			highlights = append(highlights, fmt.Sprintf("You spent about %s reading email.", formatReadingTime(readingMinutes)))
		}
		if reactionsGiven > 0 {
			highlights = append(highlights, fmt.Sprintf("You shared %s reactions.", formatCount(reactionsGiven)))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"year": year, "years": years, "timeZone": zoneName,
		"totals": map[string]any{
			"messages": total, "sent": sent, "received": received, "threads": threads,
			"unread": unread, "newsletters": newsletters, "readingMinutes": readingMinutes,
			"readingHours": float64(readingMinutes) / 60,
		},
		"topSenders": topSenders, "topRecipients": topRecipients,
		"monthly":      monthlyItems,
		"busiestMonth": map[string]any{"month": busiestMonth, "label": monthName(busiestMonth), "count": busiestMonthCount},
		"busiestDay":   map[string]any{"date": busiestDay, "count": busiestDayCount},
		"busiestHour":  map[string]any{"hour": busiestHour, "count": busiestHourCount},
		"reactions":    map[string]any{"given": reactionsGiven, "received": reactionsReceived, "top": topReactions},
		"unsubscribed": unsubscribed,
		"highlights":   highlights,
	})
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func monthName(month int) string {
	if month < 1 || month > 12 {
		return ""
	}
	return time.Month(month).String()
}

func formatCount(value int) string {
	return strconv.Itoa(value)
}

func formatReadingTime(minutes int) string {
	if minutes < 60 {
		return fmt.Sprintf("%d min", minutes)
	}
	hours := float64(minutes) / 60
	return fmt.Sprintf("%.1f hours", hours)
}

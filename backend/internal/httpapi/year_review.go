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

// Gmail labels and Sent copies can create more than one local row for the same
// message. Pulse counts each message once, preferring the standard-folder copy.
const yearReviewMessages = `(
	SELECT DISTINCT ON (account_id, COALESCE(NULLIF(message_id, ''), id)) *
	FROM public.mail_messages
	WHERE user_id=$1
	ORDER BY account_id, COALESCE(NULLIF(message_id, ''), id),
	         CASE folder WHEN 'inbox' THEN 0 WHEN 'sent' THEN 1 WHEN 'archive' THEN 2
	                     WHEN 'drafts' THEN 3 WHEN 'junk' THEN 4 WHEN 'spam' THEN 5
	                     WHEN 'trash' THEN 6 ELSE 7 END,
	         sent_at DESC
) mail_messages`

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
	scope, scopeErr := s.resolveMailReadScope(ctx, sess, requestedMailAccountID(r))
	if scopeErr != nil {
		writeError(w, http.StatusInternalServerError, "MAIL_ACCOUNT", "Could not resolve the mailbox account")
		return
	}
	withAccount := func(where string, args []any) (string, []any) {
		if scope.Unified {
			return where, args
		}
		args = append(args, scope.AccountID)
		return fmt.Sprintf("%s AND account_id=$%d", where, len(args)), args
	}

	years := []int{}
	yearQuery := `
		SELECT DISTINCT extract(year from sent_at AT TIME ZONE $2)::int
		FROM ` + yearReviewMessages + ` WHERE user_id=$1`
	yearArgs := []any{sess.User.ID, zoneName}
	if !scope.Unified {
		yearQuery += ` AND account_id=$3`
		yearArgs = append(yearArgs, scope.AccountID)
	}
	yearQuery += ` ORDER BY 1 DESC`
	if rows, err := s.store.DB.Query(ctx, yearQuery, yearArgs...); err == nil {
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
	totalWhere, totalArgs := withAccount("user_id=$1 AND sent_at >= $2 AND sent_at < $3 AND NOT draft",
		[]any{sess.User.ID, start.UTC(), end.UTC()})
	_ = s.store.DB.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE folder='sent'),
		       count(*) FILTER (WHERE folder<>'sent'),
		       count(DISTINCT thread_id),
		       count(*) FILTER (WHERE unread),
		       count(*) FILTER (WHERE unsubscribe IS NOT NULL AND unsubscribe <> '{}'::jsonb),
		       COALESCE(sum(length(body_text)), 0)
		FROM `+yearReviewMessages+`
		WHERE `+totalWhere+`
	`, totalArgs...).Scan(&total, &sent, &received, &threads, &unread, &newsletters, &chars)
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
	senderWhere, senderArgs := withAccount(
		"user_id=$1 AND sent_at >= $2 AND sent_at < $3 AND folder<>'sent' AND NOT draft AND lower(from_email::text) <> lower($4)",
		[]any{sess.User.ID, start.UTC(), end.UTC(), sess.User.Email})
	if rows, err := s.store.DB.Query(ctx, `
		SELECT COALESCE(NULLIF(from_name,''), from_email::text), lower(from_email::text), count(*)
		FROM `+yearReviewMessages+`
		WHERE `+senderWhere+`
		GROUP BY 1,2 ORDER BY count(*) DESC, 1 ASC LIMIT 5
	`, senderArgs...); err == nil {
		for rows.Next() {
			var item contactCount
			if rows.Scan(&item.Name, &item.Email, &item.Count) == nil {
				topSenders = append(topSenders, item)
			}
		}
		rows.Close()
	}
	topRecipients := []contactCount{}
	recipientWhere, recipientArgs := withAccount(
		"m.user_id=$1 AND m.sent_at >= $2 AND m.sent_at < $3 AND m.folder='sent' AND NOT m.draft AND lower(p->>'email') <> lower($4)",
		[]any{sess.User.ID, start.UTC(), end.UTC(), sess.User.Email})
	recipientWhere = strings.ReplaceAll(recipientWhere, "account_id=", "m.account_id=")
	if rows, err := s.store.DB.Query(ctx, `
		SELECT COALESCE(NULLIF(p->>'name',''), p->>'email'), lower(p->>'email'), count(*)
		FROM `+yearReviewMessages+` m,
		     jsonb_array_elements(CASE WHEN jsonb_typeof(m.to_json)='array' THEN m.to_json ELSE '[]'::jsonb END) p
		WHERE `+recipientWhere+`
		GROUP BY 1,2 ORDER BY count(*) DESC, 1 ASC LIMIT 5
	`, recipientArgs...); err == nil {
		for rows.Next() {
			var item contactCount
			if rows.Scan(&item.Name, &item.Email, &item.Count) == nil {
				topRecipients = append(topRecipients, item)
			}
		}
		rows.Close()
	}

	monthly := make([]int, 12)
	monthlySent := make([]int, 12)
	monthlyReceived := make([]int, 12)
	monthWhere, monthArgs := withAccount("user_id=$1 AND sent_at >= $2 AND sent_at < $3 AND NOT draft",
		[]any{sess.User.ID, start.UTC(), end.UTC()})
	monthArgs = append(monthArgs, zoneName)
	if rows, err := s.store.DB.Query(ctx, `
		SELECT extract(month from sent_at AT TIME ZONE $5)::int,
		       count(*) FILTER (WHERE folder='sent'),
		       count(*) FILTER (WHERE folder<>'sent')
		FROM `+yearReviewMessages+`
		WHERE `+monthWhere+`
		GROUP BY 1
	`, monthArgs...); err == nil {
		for rows.Next() {
			var month, sent, received int
			if rows.Scan(&month, &sent, &received) == nil && month >= 1 && month <= 12 {
				monthly[month-1] = sent + received
				monthlySent[month-1] = sent
				monthlyReceived[month-1] = received
			}
		}
		rows.Close()
	}
	busiestMonth, busiestMonthCount := 0, 0
	monthlyItems := make([]map[string]any, 0, 12)
	for index, count := range monthly {
		label := time.Month(index + 1).String()
		monthlyItems = append(monthlyItems, map[string]any{
			"month": index + 1, "label": label, "count": count,
			"sent": monthlySent[index], "received": monthlyReceived[index],
			"start": time.Date(year, time.Month(index+1), 1, 0, 0, 0, 0, location).Format("2006-01-02"),
			"end":   time.Date(year, time.Month(index+1), 1, 0, 0, 0, 0, location).AddDate(0, 1, 0).Format("2006-01-02"),
		})
		if count > busiestMonthCount {
			busiestMonth, busiestMonthCount = index+1, count
		}
	}
	weeklyItems := []map[string]any{}
	weekWhere, weekArgs := withAccount("user_id=$1 AND sent_at >= $2 AND sent_at < $3 AND NOT draft",
		[]any{sess.User.ID, start.UTC(), end.UTC()})
	weekArgs = append(weekArgs, zoneName)
	if rows, err := s.store.DB.Query(ctx, `
		SELECT date_trunc('week', sent_at AT TIME ZONE $5)::date AS week_start,
		       count(*) FILTER (WHERE folder='sent'),
		       count(*) FILTER (WHERE folder<>'sent')
		FROM `+yearReviewMessages+`
		WHERE `+weekWhere+`
		GROUP BY 1 ORDER BY 1
	`, weekArgs...); err == nil {
		for rows.Next() {
			var weekStart time.Time
			var sent, received int
			if rows.Scan(&weekStart, &sent, &received) == nil {
				isoYear, isoWeek := weekStart.ISOWeek()
				weeklyItems = append(weeklyItems, map[string]any{
					"week": isoWeek, "year": isoYear,
					"label": "Week of " + weekStart.Format("Jan 2"), "count": sent + received,
					"sent": sent, "received": received,
					"start": weekStart.Format("2006-01-02"),
					"end":   weekStart.AddDate(0, 0, 6).Format("2006-01-02"),
				})
			}
		}
		rows.Close()
	}
	var busiestDay string
	var busiestDayCount int
	busyWhere, busyArgs := withAccount("user_id=$1 AND sent_at >= $2 AND sent_at < $3 AND NOT draft",
		[]any{sess.User.ID, start.UTC(), end.UTC()})
	busyArgs = append(busyArgs, zoneName)
	_ = s.store.DB.QueryRow(ctx, `
		SELECT to_char(sent_at AT TIME ZONE $5, 'YYYY-MM-DD'), count(*)
		FROM `+yearReviewMessages+`
		WHERE `+busyWhere+`
		GROUP BY 1 ORDER BY count(*) DESC, 1 ASC LIMIT 1
	`, busyArgs...).Scan(&busiestDay, &busiestDayCount)
	var busiestHour, busiestHourCount int
	_ = s.store.DB.QueryRow(ctx, `
		SELECT extract(hour from sent_at AT TIME ZONE $5)::int, count(*)
		FROM `+yearReviewMessages+`
		WHERE `+busyWhere+`
		GROUP BY 1 ORDER BY count(*) DESC, 1 ASC LIMIT 1
	`, busyArgs...).Scan(&busiestHour, &busiestHourCount)
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
		"weekly":       weeklyItems,
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

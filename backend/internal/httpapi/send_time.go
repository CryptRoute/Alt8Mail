package httpapi

import (
	"fmt"
	"net/http"
	"sort"
	"time"
)

func (s *Server) registerSendTimeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/send-time-suggestions", s.requireUser(s.sendTimeSuggestions))
}

type recipientTimeProfile struct {
	Email         string
	Name          string
	OffsetMinutes int
	Known         bool
}

func (s *Server) sendTimeSuggestions(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		To []string `json:"to"`
	}
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	seen := map[string]bool{}
	emails := []string{}
	for _, raw := range body.To {
		email := normalizeEmailValue(raw)
		if !looksLikeEmail(email) || seen[email] || len(emails) >= 20 {
			continue
		}
		seen[email] = true
		emails = append(emails, email)
	}
	if len(emails) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"suggestions": []any{}, "recipients": []any{}})
		return
	}
	profiles := make([]recipientTimeProfile, 0, len(emails))
	for _, email := range emails {
		profile := recipientTimeProfile{Email: email}
		var count int
		if err := s.store.DB.QueryRow(r.Context(), `
			WITH recent AS (
				SELECT sender_utc_offset_minutes AS offset_minutes, from_name, sent_at
				FROM public.mail_messages
				WHERE user_id=$1 AND lower(from_email::text)=lower($2)
				  AND sender_utc_offset_minutes IS NOT NULL
				  AND sent_at > now() - interval '365 days'
			)
			SELECT offset_minutes, count(*), (array_agg(from_name ORDER BY sent_at DESC))[1]
			FROM recent GROUP BY offset_minutes ORDER BY count(*) DESC, offset_minutes ASC LIMIT 1
		`, sess.User.ID, email).Scan(&profile.OffsetMinutes, &count, &profile.Name); err == nil {
			profile.Known = true
		}
		profiles = append(profiles, profile)
	}

	type suggestion struct {
		SendAt        time.Time
		Recipient     string
		RecipientName string
		Known         bool
		OffsetMinutes int
		Reason        string
	}
	suggestions := []suggestion{}
	for _, profile := range profiles {
		if profile.Known {
			slot := nextRecipientWeekdaySlot(time.Now().UTC(), profile.OffsetMinutes)
			suggestions = append(suggestions, suggestion{
				SendAt: slot, Recipient: profile.Email, RecipientName: profile.Name,
				Known: true, OffsetMinutes: profile.OffsetMinutes,
				Reason: "Usually active around this time",
			})
		}
	}
	if len(suggestions) == 0 {
		location := userTimeLocation(sess.User)
		now := time.Now().In(location)
		for _, hour := range []int{9, 14} {
			if slot := nextLocalWeekdaySlot(now, hour, 30); !slot.IsZero() {
				suggestions = append(suggestions, suggestion{
					SendAt: slot.UTC(), Known: false,
					Reason: "Based on your working hours",
				})
			}
		}
	}
	sort.SliceStable(suggestions, func(i, j int) bool { return suggestions[i].SendAt.Before(suggestions[j].SendAt) })
	unique := []suggestion{}
	seenTimes := map[int64]bool{}
	for _, item := range suggestions {
		key := item.SendAt.Unix()
		if seenTimes[key] {
			continue
		}
		seenTimes[key] = true
		unique = append(unique, item)
		if len(unique) >= 4 {
			break
		}
	}
	items := []map[string]any{}
	for _, item := range unique {
		recipientLocal := ""
		if item.Known {
			zone := time.FixedZone(fmt.Sprintf("UTC%+d", item.OffsetMinutes/60), item.OffsetMinutes*60)
			recipientLocal = item.SendAt.In(zone).Format("Mon 3:04 PM")
		}
		items = append(items, map[string]any{
			"sendAt":    item.SendAt.UTC().Format(time.RFC3339),
			"recipient": item.Recipient, "recipientName": item.RecipientName,
			"known": item.Known, "offsetMinutes": item.OffsetMinutes,
			"recipientLocal": recipientLocal, "reason": item.Reason,
		})
	}
	recipientItems := []map[string]any{}
	for _, profile := range profiles {
		recipientItems = append(recipientItems, map[string]any{
			"email": profile.Email, "name": profile.Name,
			"known": profile.Known, "offsetMinutes": profile.OffsetMinutes,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": items, "recipients": recipientItems})
}

func nextRecipientWeekdaySlot(now time.Time, offsetMinutes int) time.Time {
	zone := time.FixedZone("recipient", offsetMinutes*60)
	local := now.In(zone)
	for day := 0; day < 8; day++ {
		candidate := time.Date(local.Year(), local.Month(), local.Day()+day, 9, 30, 0, 0, zone)
		if candidate.Weekday() == time.Saturday || candidate.Weekday() == time.Sunday {
			continue
		}
		if candidate.After(now.Add(10 * time.Minute)) {
			return candidate.UTC()
		}
	}
	return now.Add(24 * time.Hour)
}

func nextLocalWeekdaySlot(now time.Time, hour, minute int) time.Time {
	location := now.Location()
	for day := 0; day < 8; day++ {
		candidate := time.Date(now.Year(), now.Month(), now.Day()+day, hour, minute, 0, 0, location)
		if candidate.Weekday() == time.Saturday || candidate.Weekday() == time.Sunday {
			continue
		}
		if candidate.After(now.Add(10 * time.Minute)) {
			return candidate
		}
	}
	return time.Time{}
}

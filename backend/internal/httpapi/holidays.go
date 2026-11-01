package httpapi

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// holidayCountry describes a public-holiday calendar. month/day is the
// national day (0/0 when the country has no single fixed national day).
// easter adds Good Friday and Easter Monday, boxing adds 26 December and
// christmas adds 25 December. Every calendar always includes 1 January.
type holidayCountry struct {
	Code      string
	Name      string
	Month     int
	Day       int
	Easter    bool
	Boxing    bool
	Christmas bool
}

func holidayCatalog() []holidayCountry {
	return []holidayCountry{
		{"AF", "Afghanistan", 8, 19, false, false, false},
		{"AL", "Albania", 11, 28, true, false, true},
		{"DZ", "Algeria", 7, 5, false, false, false},
		{"AD", "Andorra", 9, 8, false, false, true},
		{"AO", "Angola", 11, 11, false, false, true},
		{"AG", "Antigua and Barbuda", 11, 1, true, true, true},
		{"AR", "Argentina", 5, 25, true, false, true},
		{"AM", "Armenia", 9, 21, false, false, false},
		{"AU", "Australia", 1, 26, true, true, true},
		{"AT", "Austria", 10, 26, true, true, true},
		{"AZ", "Azerbaijan", 5, 28, false, false, false},
		{"BS", "Bahamas", 7, 10, true, true, true},
		{"BH", "Bahrain", 12, 16, false, false, false},
		{"BD", "Bangladesh", 3, 26, false, false, false},
		{"BB", "Barbados", 11, 30, true, true, true},
		{"BY", "Belarus", 7, 3, false, false, true},
		{"BE", "Belgium", 7, 21, true, false, true},
		{"BZ", "Belize", 9, 21, false, true, true},
		{"BJ", "Benin", 8, 1, false, false, true},
		{"BT", "Bhutan", 12, 17, false, false, false},
		{"BO", "Bolivia", 8, 6, true, false, true},
		{"BA", "Bosnia and Herzegovina", 11, 25, true, false, true},
		{"BW", "Botswana", 9, 30, true, true, true},
		{"BR", "Brazil", 9, 7, true, false, true},
		{"BN", "Brunei", 2, 23, false, false, false},
		{"BG", "Bulgaria", 3, 3, true, false, true},
		{"BF", "Burkina Faso", 12, 11, false, false, true},
		{"BI", "Burundi", 7, 1, false, false, true},
		{"KH", "Cambodia", 11, 9, false, false, false},
		{"CM", "Cameroon", 5, 20, false, false, true},
		{"CA", "Canada", 7, 1, true, true, true},
		{"CV", "Cape Verde", 7, 5, false, false, true},
		{"CF", "Central African Republic", 12, 1, false, false, true},
		{"TD", "Chad", 8, 11, false, false, true},
		{"CL", "Chile", 9, 18, true, false, true},
		{"CN", "China", 10, 1, false, false, false},
		{"CO", "Colombia", 7, 20, true, false, true},
		{"KM", "Comoros", 7, 6, false, false, false},
		{"CG", "Congo", 8, 15, false, false, true},
		{"CD", "Congo (Democratic Republic of)", 6, 30, false, false, true},
		{"CR", "Costa Rica", 9, 15, true, false, true},
		{"HR", "Croatia", 5, 30, true, false, true},
		{"CU", "Cuba", 1, 1, false, false, true},
		{"CY", "Cyprus", 10, 1, true, true, true},
		{"CZ", "Czechia", 10, 28, true, true, true},
		{"DK", "Denmark", 6, 5, true, true, true},
		{"DJ", "Djibouti", 6, 27, false, false, false},
		{"DM", "Dominica", 11, 3, true, true, true},
		{"DO", "Dominican Republic", 2, 27, true, false, true},
		{"EC", "Ecuador", 8, 10, true, false, true},
		{"EG", "Egypt", 7, 23, false, false, false},
		{"SV", "El Salvador", 9, 15, true, false, true},
		{"GQ", "Equatorial Guinea", 10, 12, false, false, true},
		{"ER", "Eritrea", 5, 24, false, false, false},
		{"EE", "Estonia", 2, 24, true, true, true},
		{"SZ", "Eswatini", 9, 6, true, true, true},
		{"ET", "Ethiopia", 5, 28, false, false, true},
		{"FJ", "Fiji", 10, 10, true, true, true},
		{"FI", "Finland", 12, 6, true, true, true},
		{"FR", "France", 7, 14, true, false, true},
		{"GA", "Gabon", 8, 17, false, false, true},
		{"GM", "Gambia", 2, 18, false, false, true},
		{"GE", "Georgia", 5, 26, true, false, true},
		{"DE", "Germany", 10, 3, true, true, true},
		{"GH", "Ghana", 3, 6, true, true, true},
		{"GR", "Greece", 3, 25, true, false, true},
		{"GD", "Grenada", 2, 7, true, true, true},
		{"GT", "Guatemala", 9, 15, true, false, true},
		{"GN", "Guinea", 10, 2, false, false, true},
		{"GW", "Guinea-Bissau", 9, 24, false, false, true},
		{"GY", "Guyana", 5, 26, true, true, true},
		{"HT", "Haiti", 1, 1, true, false, true},
		{"HN", "Honduras", 9, 15, true, false, true},
		{"HU", "Hungary", 8, 20, true, true, true},
		{"IS", "Iceland", 6, 17, true, true, true},
		{"IN", "India", 8, 15, false, false, true},
		{"ID", "Indonesia", 8, 17, false, false, true},
		{"IR", "Iran", 2, 11, false, false, false},
		{"IQ", "Iraq", 10, 3, false, false, false},
		{"IE", "Ireland", 3, 17, true, true, true},
		{"IL", "Israel", 5, 14, false, false, false},
		{"IT", "Italy", 6, 2, true, true, true},
		{"JM", "Jamaica", 8, 6, true, true, true},
		{"JP", "Japan", 2, 11, false, false, false},
		{"JO", "Jordan", 5, 25, false, false, false},
		{"KZ", "Kazakhstan", 12, 16, false, false, false},
		{"KE", "Kenya", 12, 12, true, true, true},
		{"KI", "Kiribati", 7, 12, true, true, true},
		{"KW", "Kuwait", 2, 25, false, false, false},
		{"KG", "Kyrgyzstan", 8, 31, false, false, false},
		{"LA", "Laos", 12, 2, false, false, false},
		{"LV", "Latvia", 11, 18, true, true, true},
		{"LB", "Lebanon", 11, 22, false, false, true},
		{"LS", "Lesotho", 10, 4, true, true, true},
		{"LR", "Liberia", 7, 26, false, false, true},
		{"LY", "Libya", 12, 24, false, false, false},
		{"LI", "Liechtenstein", 8, 15, true, true, true},
		{"LT", "Lithuania", 2, 16, true, true, true},
		{"LU", "Luxembourg", 6, 23, true, true, true},
		{"MG", "Madagascar", 6, 26, true, false, true},
		{"MW", "Malawi", 7, 6, true, true, true},
		{"MY", "Malaysia", 8, 31, false, false, true},
		{"MV", "Maldives", 7, 26, false, false, false},
		{"ML", "Mali", 9, 22, false, false, false},
		{"MT", "Malta", 9, 21, true, false, true},
		{"MH", "Marshall Islands", 5, 1, false, false, true},
		{"MR", "Mauritania", 11, 28, false, false, false},
		{"MU", "Mauritius", 3, 12, false, false, true},
		{"MX", "Mexico", 9, 16, false, false, true},
		{"FM", "Micronesia", 11, 3, false, false, true},
		{"MD", "Moldova", 8, 27, true, false, true},
		{"MC", "Monaco", 11, 19, true, false, true},
		{"MN", "Mongolia", 7, 11, false, false, false},
		{"ME", "Montenegro", 7, 13, true, false, true},
		{"MA", "Morocco", 7, 30, false, false, false},
		{"MZ", "Mozambique", 6, 25, false, false, true},
		{"MM", "Myanmar", 1, 4, false, false, true},
		{"NA", "Namibia", 3, 21, true, true, true},
		{"NR", "Nauru", 1, 31, true, true, true},
		{"NP", "Nepal", 9, 20, false, false, false},
		{"NL", "Netherlands", 4, 27, true, true, true},
		{"NZ", "New Zealand", 2, 6, true, true, true},
		{"NI", "Nicaragua", 9, 15, true, false, true},
		{"NE", "Niger", 12, 18, false, false, false},
		{"NG", "Nigeria", 10, 1, true, true, true},
		{"MK", "North Macedonia", 9, 8, true, false, true},
		{"NO", "Norway", 5, 17, true, true, true},
		{"OM", "Oman", 11, 18, false, false, false},
		{"PK", "Pakistan", 8, 14, false, false, false},
		{"PW", "Palau", 10, 1, false, false, true},
		{"PA", "Panama", 11, 3, true, false, true},
		{"PG", "Papua New Guinea", 9, 16, true, true, true},
		{"PY", "Paraguay", 5, 14, true, false, true},
		{"PE", "Peru", 7, 28, true, false, true},
		{"PH", "Philippines", 6, 12, true, false, true},
		{"PL", "Poland", 11, 11, true, true, true},
		{"PT", "Portugal", 6, 10, true, false, true},
		{"QA", "Qatar", 12, 18, false, false, false},
		{"RO", "Romania", 12, 1, true, true, true},
		{"RU", "Russia", 6, 12, false, false, true},
		{"RW", "Rwanda", 7, 1, false, false, true},
		{"KN", "Saint Kitts and Nevis", 9, 19, true, true, true},
		{"LC", "Saint Lucia", 2, 22, true, true, true},
		{"VC", "Saint Vincent and the Grenadines", 10, 27, true, true, true},
		{"WS", "Samoa", 6, 1, true, true, true},
		{"SM", "San Marino", 9, 3, true, true, true},
		{"SA", "Saudi Arabia", 9, 23, false, false, false},
		{"SN", "Senegal", 4, 4, true, false, true},
		{"RS", "Serbia", 2, 15, true, false, true},
		{"SC", "Seychelles", 6, 29, true, false, true},
		{"SL", "Sierra Leone", 4, 27, true, true, true},
		{"SG", "Singapore", 8, 9, false, false, true},
		{"SK", "Slovakia", 9, 1, true, true, true},
		{"SI", "Slovenia", 6, 25, true, true, true},
		{"SB", "Solomon Islands", 7, 7, true, true, true},
		{"SO", "Somalia", 7, 1, false, false, false},
		{"ZA", "South Africa", 4, 27, true, true, true},
		{"KR", "South Korea", 8, 15, false, false, true},
		{"SS", "South Sudan", 7, 9, false, false, true},
		{"ES", "Spain", 10, 12, true, false, true},
		{"LK", "Sri Lanka", 2, 4, false, false, true},
		{"SD", "Sudan", 1, 1, false, false, false},
		{"SR", "Suriname", 11, 25, false, false, true},
		{"SE", "Sweden", 6, 6, true, true, true},
		{"CH", "Switzerland", 8, 1, true, true, true},
		{"SY", "Syria", 4, 17, false, false, false},
		{"TW", "Taiwan", 10, 10, false, false, false},
		{"TJ", "Tajikistan", 9, 9, false, false, false},
		{"TZ", "Tanzania", 12, 9, true, true, true},
		{"TH", "Thailand", 12, 5, false, false, false},
		{"TL", "Timor-Leste", 5, 20, false, false, true},
		{"TG", "Togo", 4, 27, true, false, true},
		{"TO", "Tonga", 11, 4, true, true, true},
		{"TT", "Trinidad and Tobago", 8, 31, true, true, true},
		{"TN", "Tunisia", 3, 20, false, false, false},
		{"TR", "Turkey", 10, 29, false, false, false},
		{"TM", "Turkmenistan", 9, 27, false, false, false},
		{"TV", "Tuvalu", 10, 1, false, true, true},
		{"UG", "Uganda", 10, 9, true, true, true},
		{"UA", "Ukraine", 8, 24, true, true, true},
		{"AE", "United Arab Emirates", 12, 2, false, false, false},
		{"GB", "United Kingdom", 0, 0, true, true, true},
		{"US", "United States", 7, 4, false, false, true},
		{"UY", "Uruguay", 8, 25, true, false, true},
		{"UZ", "Uzbekistan", 9, 1, false, false, false},
		{"VU", "Vanuatu", 7, 30, true, true, true},
		{"VA", "Vatican City", 3, 13, true, true, true},
		{"VE", "Venezuela", 7, 5, true, false, true},
		{"VN", "Vietnam", 9, 2, false, false, false},
		{"YE", "Yemen", 5, 22, false, false, false},
		{"ZM", "Zambia", 10, 24, false, false, true},
		{"ZW", "Zimbabwe", 4, 18, true, true, true},
		{"CHR", "Christian Religious Holidays", 0, 0, true, true, true},
	}
}

func findHolidayCountry(code string) (holidayCountry, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, country := range holidayCatalog() {
		if country.Code == code {
			return country, true
		}
	}
	return holidayCountry{}, false
}

func easterSunday(year int) time.Time {
	a := year % 19
	b := year / 100
	c := year % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := (h + l - 7*m + 114) / 31
	day := ((h + l - 7*m + 114) % 31) + 1
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

func holidayEvents(country holidayCountry, year int) []parsedCalendarEvent {
	events := []parsedCalendarEvent{}
	add := func(day time.Time, title string) {
		events = append(events, parsedCalendarEvent{
			UID:          fmt.Sprintf("holiday-%s-%d-%s", strings.ToLower(country.Code), day.Year(), day.Format("0102")),
			Title:        title,
			Description:  fmt.Sprintf("%s · %s", title, country.Name),
			Location:     country.Name,
			StartsAt:     day,
			EndsAt:       day.AddDate(0, 0, 1).Add(-time.Second),
			AllDay:       true,
			Timezone:     "UTC",
			Status:       "confirmed",
			Transparency: "free",
		})
	}
	for _, targetYear := range []int{year - 1, year, year + 1} {
		add(time.Date(targetYear, 1, 1, 0, 0, 0, 0, time.UTC), "New Year's Day")
		if country.Month > 0 && !(country.Month == 1 && country.Day == 1) {
			add(time.Date(targetYear, time.Month(country.Month), country.Day, 0, 0, 0, 0, time.UTC), country.Name+" National Day")
		}
		if country.Easter {
			easter := easterSunday(targetYear)
			add(easter.AddDate(0, 0, -2), "Good Friday")
			add(easter.AddDate(0, 0, 1), "Easter Monday")
		}
		if country.Christmas {
			add(time.Date(targetYear, 12, 25, 0, 0, 0, 0, time.UTC), "Christmas Day")
		}
		if country.Boxing {
			add(time.Date(targetYear, 12, 26, 0, 0, 0, 0, time.UTC), "Boxing Day")
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].StartsAt.Before(events[j].StartsAt) })
	return events
}

func (s *Server) getHolidayCatalog(w http.ResponseWriter, r *http.Request) {
	countries := holidayCatalog()
	sort.Slice(countries, func(i, j int) bool { return countries[i].Name < countries[j].Name })
	out := make([]map[string]any, 0, len(countries))
	for _, country := range countries {
		out = append(out, map[string]any{"code": country.Code, "name": country.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"countries": out, "count": len(out)})
}

func (s *Server) addHolidayCalendar(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Country string `json:"country"`
		Year    int    `json:"year"`
	}
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	country, ok := findHolidayCountry(body.Country)
	if !ok {
		writeError(w, http.StatusBadRequest, "HOLIDAY_COUNTRY", "Choose a country from the holiday catalogue")
		return
	}
	year := body.Year
	if year < 1970 || year > 2100 {
		year = time.Now().Year()
	}
	calendarID := newID("holiday")
	name := country.Name + " holidays"
	sourceKey := "holiday:" + country.Code
	// Replace an existing holiday calendar for this country instead of stacking
	// duplicates in the sidebar.
	var existing string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT id FROM calendars WHERE user_id=$1 AND kind='holiday' AND source_key=$2 LIMIT 1
	`, sess.User.ID, sourceKey).Scan(&existing); err == nil {
		calendarID = existing
		tx, err := s.store.DB.Begin(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not refresh the holiday calendar")
			return
		}
		defer tx.Rollback(r.Context())
		_, _ = tx.Exec(r.Context(), `DELETE FROM calendar_events WHERE user_id=$1 AND calendar_id=$2`, sess.User.ID, calendarID)
		if err := s.insertParsedCalendarEvents(r.Context(), tx, sess.User, calendarID, sourceKey, holidayEvents(country, year)); err != nil {
			writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not import the holidays")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not commit the holiday calendar")
			return
		}
		s.publishRealtime("calendar")
		s.writeCalendars(w, r)
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not add the holiday calendar")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO calendars (id, tenant_id, user_id, name, color, kind, source_key)
		VALUES ($1,$2,$3,$4,'#8a5a2b','holiday',$5)
	`, calendarID, sess.User.TenantID, sess.User.ID, name, sourceKey); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not create the holiday calendar")
		return
	}
	if err := s.insertParsedCalendarEvents(r.Context(), tx, sess.User, calendarID, sourceKey, holidayEvents(country, year)); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not import the holidays")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not commit the holiday calendar")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.holidays_added", name, fmt.Sprintf("%d holidays", len(holidayEvents(country, year))))
	s.publishRealtime("calendar")
	s.writeCalendars(w, r)
}

func (s *Server) addBirthdayCalendar(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, name, email::text, birthday FROM contacts
		WHERE user_id=$1 AND birthday IS NOT NULL
		ORDER BY birthday
	`, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not load birthdays")
		return
	}
	defer rows.Close()
	type birthday struct {
		id, name, email string
		date            time.Time
	}
	items := []birthday{}
	for rows.Next() {
		var id, name, email string
		var date time.Time
		if rows.Scan(&id, &name, &email, &date) == nil {
			items = append(items, birthday{id: id, name: name, email: email, date: date})
		}
	}
	calendarID := newID("birthday")
	var existing string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT id FROM calendars WHERE user_id=$1 AND kind='birthday' LIMIT 1
	`, sess.User.ID).Scan(&existing); err == nil {
		calendarID = existing
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not add the birthday calendar")
		return
	}
	defer tx.Rollback(r.Context())
	if existing == "" {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO calendars (id, tenant_id, user_id, name, color, kind, source_key)
			VALUES ($1,$2,$3,'Birthdays','#a23b72','birthday','birthdays')
		`, calendarID, sess.User.TenantID, sess.User.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not create the birthday calendar")
			return
		}
	} else {
		if _, err := tx.Exec(r.Context(), `DELETE FROM calendar_events WHERE user_id=$1 AND calendar_id=$2`, sess.User.ID, calendarID); err != nil {
			writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not refresh the birthday calendar")
			return
		}
	}
	year := time.Now().Year()
	events := []parsedCalendarEvent{}
	for _, item := range items {
		start := time.Date(year, item.date.Month(), item.date.Day(), 0, 0, 0, 0, time.UTC)
		name := strings.TrimSpace(item.name)
		if name == "" {
			name = strings.Split(item.email, "@")[0]
		}
		events = append(events, parsedCalendarEvent{
			UID: "birthday-" + item.id, Title: name + "'s birthday",
			Description: "Birthday from your Alt8Mail contacts.", Location: "",
			StartsAt: start, EndsAt: start.AddDate(0, 0, 1).Add(-time.Second),
			AllDay: true, Timezone: "UTC", Status: "confirmed", Transparency: "free",
			RecurrenceRule: "FREQ=YEARLY",
		})
	}
	if err := s.insertParsedCalendarEvents(r.Context(), tx, sess.User, calendarID, "birthdays", events); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not import the birthdays")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not commit the birthday calendar")
		return
	}
	s.publishRealtime("calendar")
	s.writeCalendars(w, r)
}

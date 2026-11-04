package httpapi

import (
	"fmt"
	"strings"
)

type calendarInviteStrings struct {
	Lang, Dir                                                                                  string
	SubjectInvitation, SubjectUpdated, SubjectCancelled                                        string
	Eyebrow, InvitedIntro, UpdatedIntro, UpdatedNotice                                         string
	CancelledHeading, CancelledIntro, NoAction                                                 string
	WhenLabel, WhereLabel, OrganizerLabel, JoinLabel                                           string
	YesButton, MaybeButton, NoButton, RsvpHint                                                 string
	AttachedNote, AllDay, TimeToBeConfirmed, OpenAttached, OnBehalf, FeedLabel, Footer, SentBy string
}

var calendarInviteCatalog = map[string]calendarInviteStrings{
	"en": {
		Lang: "en", Dir: "ltr",
		SubjectInvitation: "Invitation", SubjectUpdated: "Updated invitation", SubjectCancelled: "Cancelled event",
		Eyebrow: "Alt8Mail calendar", InvitedIntro: "You have been invited to a meeting.",
		UpdatedIntro:     "The organizer changed this invitation. Your previous response is cleared, so please respond again.",
		UpdatedNotice:    "The organizer changed this meeting. Your earlier response no longer applies — please respond to the updated time.",
		CancelledHeading: "Meeting cancelled", CancelledIntro: "This event has been cancelled. No further action is needed.",
		NoAction: "No further action is needed.", WhenLabel: "When", WhereLabel: "Where",
		OrganizerLabel: "Organizer", JoinLabel: "Join", YesButton: "Yes, I'll attend", MaybeButton: "Maybe",
		NoButton:     "No, I can't",
		RsvpHint:     "These links are signed for this invitation and expire automatically. Use the newest invitation if the time changes.",
		AttachedNote: "A calendar file is attached. It works with Gmail, Outlook, Apple Calendar, Thunderbird and any iTIP-compatible calendar.",
		AllDay:       "all day", TimeToBeConfirmed: "Time to be confirmed",
		OpenAttached: "Open the attached invitation in your calendar to respond.",
		OnBehalf:     "Sent by %s on behalf of %s.", FeedLabel: "Subscribe to this calendar", Footer: "Alt8Mail Calendar",
		SentBy: "Sent by",
	},
	"de": {
		Lang: "de", Dir: "ltr",
		SubjectInvitation: "Einladung", SubjectUpdated: "Aktualisierte Einladung", SubjectCancelled: "Abgesagter Termin",
		Eyebrow: "Alt8Mail Kalender", InvitedIntro: "Sie wurden zu einem Termin eingeladen.",
		UpdatedIntro:     "Der Organisator hat diese Einladung geändert. Ihre frühere Antwort wurde zurückgesetzt; bitte antworten Sie erneut.",
		UpdatedNotice:    "Der Organisator hat diesen Termin geändert. Ihre frühere Antwort gilt nicht mehr — bitte antworten Sie für die neue Zeit.",
		CancelledHeading: "Termin abgesagt", CancelledIntro: "Dieser Termin wurde abgesagt. Es ist nichts weiter zu tun.",
		NoAction: "Es ist nichts weiter zu tun.", WhenLabel: "Wann", WhereLabel: "Ort",
		OrganizerLabel: "Organisator", JoinLabel: "Teilnehmen", YesButton: "Ja, ich nehme teil", MaybeButton: "Vielleicht",
		NoButton:     "Nein, ich kann nicht",
		RsvpHint:     "Diese Links sind für diese Einladung signiert und laufen automatisch ab. Verwenden Sie bei einer Zeitänderung die neueste Einladung.",
		AttachedNote: "Eine Kalenderdatei ist angehängt. Sie funktioniert mit Gmail, Outlook, Apple Kalender, Thunderbird und jedem iTIP-kompatiblen Kalender.",
		AllDay:       "ganztägig", TimeToBeConfirmed: "Uhrzeit wird noch bestätigt",
		OpenAttached: "Öffnen Sie die angehängte Einladung in Ihrem Kalender, um zu antworten.",
		OnBehalf:     "Gesendet von %s im Auftrag von %s.", FeedLabel: "Diesen Kalender abonnieren", Footer: "Alt8Mail Kalender",
		SentBy: "Gesendet von",
	},
	"fr": {
		Lang: "fr", Dir: "ltr",
		SubjectInvitation: "Invitation", SubjectUpdated: "Invitation mise à jour", SubjectCancelled: "Événement annulé",
		Eyebrow: "Calendrier Alt8Mail", InvitedIntro: "Vous avez été invité à une réunion.",
		UpdatedIntro:     "L’organisateur a modifié cette invitation. Votre réponse précédente a été réinitialisée ; veuillez répondre à nouveau.",
		UpdatedNotice:    "L’organisateur a modifié cette réunion. Votre réponse précédente ne s’applique plus — veuillez répondre pour le nouvel horaire.",
		CancelledHeading: "Réunion annulée", CancelledIntro: "Cet événement a été annulé. Aucune action n’est nécessaire.",
		NoAction: "Aucune action n’est nécessaire.", WhenLabel: "Quand", WhereLabel: "Lieu",
		OrganizerLabel: "Organisateur", JoinLabel: "Rejoindre", YesButton: "Oui, je participe", MaybeButton: "Peut-être",
		NoButton:     "Non, je ne peux pas",
		RsvpHint:     "Ces liens sont signés pour cette invitation et expirent automatiquement. Utilisez l’invitation la plus récente si l’horaire change.",
		AttachedNote: "Un fichier de calendrier est joint. Il fonctionne avec Gmail, Outlook, Apple Calendrier, Thunderbird et tout calendrier compatible iTIP.",
		AllDay:       "toute la journée", TimeToBeConfirmed: "Horaire à confirmer",
		OpenAttached: "Ouvrez l’invitation jointe dans votre calendrier pour répondre.",
		OnBehalf:     "Envoyé par %s au nom de %s.", FeedLabel: "S’abonner à ce calendrier", Footer: "Calendrier Alt8Mail",
		SentBy: "Envoyé par",
	},
	"es": {
		Lang: "es", Dir: "ltr",
		SubjectInvitation: "Invitación", SubjectUpdated: "Invitación actualizada", SubjectCancelled: "Evento cancelado",
		Eyebrow: "Calendario de Alt8Mail", InvitedIntro: "Te han invitado a una reunión.",
		UpdatedIntro:     "El organizador ha cambiado esta invitación. Tu respuesta anterior se ha borrado; vuelve a responder.",
		UpdatedNotice:    "El organizador ha cambiado esta reunión. Tu respuesta anterior ya no se aplica; responde para el nuevo horario.",
		CancelledHeading: "Reunión cancelada", CancelledIntro: "Este evento se ha cancelado. No es necesario hacer nada más.",
		NoAction: "No es necesario hacer nada más.", WhenLabel: "Cuándo", WhereLabel: "Dónde",
		OrganizerLabel: "Organizador", JoinLabel: "Unirse", YesButton: "Sí, asistiré", MaybeButton: "Quizá",
		NoButton:     "No podré",
		RsvpHint:     "Estos enlaces están firmados para esta invitación y caducan automáticamente. Usa la invitación más reciente si cambia la hora.",
		AttachedNote: "Se adjunta un archivo de calendario. Funciona con Gmail, Outlook, Apple Calendario, Thunderbird y cualquier calendario compatible con iTIP.",
		AllDay:       "todo el día", TimeToBeConfirmed: "Hora por confirmar",
		OpenAttached: "Abre la invitación adjunta en tu calendario para responder.",
		OnBehalf:     "Enviado por %s en nombre de %s.", FeedLabel: "Suscribirse a este calendario", Footer: "Calendario de Alt8Mail",
		SentBy: "Enviado por",
	},
	"pt": {
		Lang: "pt", Dir: "ltr",
		SubjectInvitation: "Convite", SubjectUpdated: "Convite atualizado", SubjectCancelled: "Evento cancelado",
		Eyebrow: "Calendário Alt8Mail", InvitedIntro: "Você foi convidado para uma reunião.",
		UpdatedIntro:     "O organizador alterou este convite. Sua resposta anterior foi apagada; responda novamente.",
		UpdatedNotice:    "O organizador alterou esta reunião. Sua resposta anterior não vale mais — responda para o novo horário.",
		CancelledHeading: "Reunião cancelada", CancelledIntro: "Este evento foi cancelado. Nenhuma ação é necessária.",
		NoAction: "Nenhuma ação é necessária.", WhenLabel: "Quando", WhereLabel: "Onde",
		OrganizerLabel: "Organizador", JoinLabel: "Entrar", YesButton: "Sim, vou participar", MaybeButton: "Talvez",
		NoButton:     "Não poderei",
		RsvpHint:     "Estes links são assinados para este convite e expiram automaticamente. Use o convite mais recente se o horário mudar.",
		AttachedNote: "Um arquivo de calendário está anexado. Ele funciona com Gmail, Outlook, Apple Calendário, Thunderbird e qualquer calendário compatível com iTIP.",
		AllDay:       "dia inteiro", TimeToBeConfirmed: "Horário a confirmar",
		OpenAttached: "Abra o convite anexo no seu calendário para responder.",
		OnBehalf:     "Enviado por %s em nome de %s.", FeedLabel: "Assinar este calendário", Footer: "Calendário Alt8Mail",
		SentBy: "Enviado por",
	},
	"nl": {
		Lang: "nl", Dir: "ltr",
		SubjectInvitation: "Uitnodiging", SubjectUpdated: "Bijgewerkte uitnodiging", SubjectCancelled: "Afspraak geannuleerd",
		Eyebrow: "Alt8Mail-agenda", InvitedIntro: "Je bent uitgenodigd voor een vergadering.",
		UpdatedIntro:     "De organisator heeft deze uitnodiging gewijzigd. Je eerdere antwoord is gewist; reageer opnieuw.",
		UpdatedNotice:    "De organisator heeft deze vergadering gewijzigd. Je eerdere antwoord geldt niet meer — reageer voor de nieuwe tijd.",
		CancelledHeading: "Vergadering geannuleerd", CancelledIntro: "Deze afspraak is geannuleerd. Er is niets meer nodig.",
		NoAction: "Er is niets meer nodig.", WhenLabel: "Wanneer", WhereLabel: "Waar",
		OrganizerLabel: "Organisator", JoinLabel: "Deelnemen", YesButton: "Ja, ik kom", MaybeButton: "Misschien",
		NoButton:     "Nee, dat lukt niet",
		RsvpHint:     "Deze links zijn ondertekend voor deze uitnodiging en verlopen automatisch. Gebruik de nieuwste uitnodiging als de tijd verandert.",
		AttachedNote: "Er is een agendabestand bijgevoegd. Het werkt met Gmail, Outlook, Apple Agenda, Thunderbird en elke iTIP-compatibele agenda.",
		AllDay:       "hele dag", TimeToBeConfirmed: "Tijd wordt nog bevestigd",
		OpenAttached: "Open de bijgevoegde uitnodiging in je agenda om te reageren.",
		OnBehalf:     "Verzonden door %s namens %s.", FeedLabel: "Deze agenda abonneren", Footer: "Alt8Mail-agenda",
		SentBy: "Verzonden door",
	},
	"it": {
		Lang: "it", Dir: "ltr",
		SubjectInvitation: "Invito", SubjectUpdated: "Invito aggiornato", SubjectCancelled: "Evento annullato",
		Eyebrow: "Calendario Alt8Mail", InvitedIntro: "Sei stato invitato a una riunione.",
		UpdatedIntro:     "L’organizzatore ha modificato questo invito. La tua risposta precedente è stata azzerata; rispondi di nuovo.",
		UpdatedNotice:    "L’organizzatore ha modificato questa riunione. La tua risposta precedente non vale più: rispondi per il nuovo orario.",
		CancelledHeading: "Riunione annullata", CancelledIntro: "Questo evento è stato annullato. Non è necessaria alcuna azione.",
		NoAction: "Non è necessaria alcuna azione.", WhenLabel: "Quando", WhereLabel: "Dove",
		OrganizerLabel: "Organizzatore", JoinLabel: "Partecipa", YesButton: "Sì, parteciperò", MaybeButton: "Forse",
		NoButton:     "No, non posso",
		RsvpHint:     "Questi link sono firmati per questo invito e scadono automaticamente. Usa l’invito più recente se l’orario cambia.",
		AttachedNote: "È allegato un file di calendario. Funziona con Gmail, Outlook, Apple Calendario, Thunderbird e qualsiasi calendario compatibile iTIP.",
		AllDay:       "tutto il giorno", TimeToBeConfirmed: "Orario da confermare",
		OpenAttached: "Apri l’invito allegato nel tuo calendario per rispondere.",
		OnBehalf:     "Inviato da %s per conto di %s.", FeedLabel: "Iscriviti a questo calendario", Footer: "Calendario Alt8Mail",
		SentBy: "Inviato da",
	},
	"ar": {
		Lang: "ar", Dir: "rtl",
		SubjectInvitation: "دعوة", SubjectUpdated: "دعوة محدّثة", SubjectCancelled: "حدث ملغى",
		Eyebrow: "تقويم Alt8Mail", InvitedIntro: "تمت دعوتك إلى اجتماع.",
		UpdatedIntro:     "غيّر المنظّم هذه الدعوة. تمت إعادة تعيين ردّك السابق، لذا يرجى الرد مرة أخرى.",
		UpdatedNotice:    "غيّر المنظّم هذا الاجتماع. لم يعد ردّك السابق ساريًا — يرجى الرد على الوقت المحدّث.",
		CancelledHeading: "تم إلغاء الاجتماع", CancelledIntro: "تم إلغاء هذا الحدث. لا يلزم اتخاذ أي إجراء.",
		NoAction: "لا يلزم اتخاذ أي إجراء.", WhenLabel: "الوقت", WhereLabel: "المكان",
		OrganizerLabel: "المنظّم", JoinLabel: "الانضمام", YesButton: "نعم، سأحضر", MaybeButton: "ربما",
		NoButton:     "لا، لا أستطيع",
		RsvpHint:     "هذه الروابط موقّعة لهذه الدعوة وتنتهي صلاحيتها تلقائيًا. استخدم أحدث دعوة إذا تغيّر الوقت.",
		AttachedNote: "تم إرفاق ملف تقويم. يعمل مع Gmail وOutlook وApple Calendar وThunderbird وأي تقويم متوافق مع iTIP.",
		AllDay:       "طوال اليوم", TimeToBeConfirmed: "سيتم تأكيد الوقت",
		OpenAttached: "افتح الدعوة المرفقة في التقويم للرد.",
		OnBehalf:     "أُرسلت من %s نيابةً عن %s.", FeedLabel: "الاشتراك في هذا التقويم", Footer: "تقويم Alt8Mail",
		SentBy: "أُرسلت من",
	},
}

func calendarInviteLanguage(user userRecord) string {
	preferences := preferencesFor(user)
	language := strings.ToLower(strings.TrimSpace(fmt.Sprint(preferences["language"])))
	if language == "" {
		language = "en"
	}
	base := strings.SplitN(language, "-", 2)[0]
	if _, ok := calendarInviteCatalog[base]; ok {
		return base
	}
	return "en"
}

func calendarInviteStringsFor(user userRecord) calendarInviteStrings {
	return calendarInviteCatalog[calendarInviteLanguage(user)]
}

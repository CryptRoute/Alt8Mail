package control

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var queueIDPattern = regexp.MustCompile(`^[A-Fa-f0-9]{1,32}$`)

type queueRecipient struct {
	Address     string `json:"address"`
	DelayReason string `json:"delay_reason"`
}

type queueItem struct {
	QueueName   string           `json:"queueName"`
	QueueID     string           `json:"queueId"`
	ArrivalTime int64            `json:"arrivalTime"`
	MessageSize int64            `json:"messageSize"`
	Sender      string           `json:"sender"`
	Recipients  []queueRecipient `json:"recipients"`
	ArrivalText string           `json:"arrivalText"`
}

func (s *Server) postfixQueue(ctx context.Context) ([]queueItem, error) {
	result, err := runCommand(ctx, 15*time.Second, "postqueue", "-j")
	if err != nil {
		// An empty queue makes postqueue -j exit non-zero on some Postfix
		// releases while still being healthy.
		if strings.TrimSpace(result.Stdout) == "" && strings.TrimSpace(result.Stderr) == "" {
			return []queueItem{}, nil
		}
		return nil, fmt.Errorf("postqueue: %s", strings.TrimSpace(result.Stderr))
	}
	items := []queueItem{}
	scanner := bufio.NewScanner(strings.NewReader(result.Stdout))
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw struct {
			QueueName   string           `json:"queue_name"`
			QueueID     string           `json:"queue_id"`
			ArrivalTime int64            `json:"arrival_time"`
			MessageSize int64            `json:"message_size"`
			Sender      string           `json:"sender"`
			Recipients  []queueRecipient `json:"recipients"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		arrival := time.Unix(raw.ArrivalTime, 0).UTC()
		items = append(items, queueItem{
			QueueName: raw.QueueName, QueueID: raw.QueueID, ArrivalTime: raw.ArrivalTime,
			MessageSize: raw.MessageSize, Sender: raw.Sender, Recipients: raw.Recipients,
			ArrivalText: arrival.Format(time.RFC3339),
		})
	}
	return items, nil
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	items, err := s.postfixQueue(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": items, "count": len(items), "checkedAt": nowRFC3339()})
}

func (s *Server) handleQueueDetail(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := strings.TrimSpace(pathID(r))
	if !queueIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "QUEUE_ID", "Invalid queue id")
		return
	}
	items, _ := s.postfixQueue(r.Context())
	var found *queueItem
	for i := range items {
		if strings.EqualFold(items[i].QueueID, id) {
			found = &items[i]
			break
		}
	}
	result, err := runCommand(r.Context(), 15*time.Second, "postcat", "-q", "-v", id)
	if err != nil && found == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "That queue message is no longer present")
		return
	}
	detail := result.Stdout
	if detail == "" {
		detail = result.Stderr
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "message": found, "transcript": detail})
}

func (s *Server) handleQueueFlush(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := strings.TrimSpace(pathID(r))
	if !queueIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "QUEUE_ID", "Invalid queue id")
		return
	}
	result, err := runCommand(r.Context(), 20*time.Second, "postqueue", "-i", id)
	if err != nil {
		writeError(w, http.StatusBadGateway, "QUEUE", "Postfix could not flush that message: "+strings.TrimSpace(result.Stderr))
		return
	}
	s.actorAudit(r, sess, "delivery.queue_flush", id, "Requested immediate delivery for a queue message",
		map[string]any{"output": strings.TrimSpace(result.Stdout)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "output": strings.TrimSpace(result.Stdout)})
}

func (s *Server) handleQueueDelete(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := strings.TrimSpace(pathID(r))
	if !queueIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "QUEUE_ID", "Invalid queue id")
		return
	}
	result, err := runCommand(r.Context(), 20*time.Second, "postsuper", "-d", id)
	if err != nil {
		writeError(w, http.StatusBadGateway, "QUEUE", "Postfix could not delete that message: "+strings.TrimSpace(result.Stderr))
		return
	}
	s.actorAudit(r, sess, "delivery.queue_delete", id, "Deleted a message from the Postfix queue", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "output": strings.TrimSpace(result.Stdout)})
}

func (s *Server) handleQueueDeleteAll(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if strings.TrimSpace(body.Confirm) != "DELETE ALL" {
		writeError(w, http.StatusBadRequest, "CONFIRM", "Type DELETE ALL to remove every queued message")
		return
	}
	result, err := runCommand(r.Context(), 30*time.Second, "postsuper", "-d", "ALL")
	if err != nil {
		writeError(w, http.StatusBadGateway, "QUEUE", "Postfix could not clear the queue: "+strings.TrimSpace(result.Stderr))
		return
	}
	s.actorAudit(r, sess, "delivery.queue_delete_all", "Postfix queue", "Deleted every message in the Postfix queue", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": strings.TrimSpace(result.Stdout)})
}

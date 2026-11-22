package dashboard

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mishankov/llm-meter/internal/ledger"
)

const (
	cookieName      = "llm_meter_session"
	sessionDuration = 24 * time.Hour
)

type server struct {
	store   *ledger.Store
	digest  [32]byte
	signKey [32]byte
	secure  bool
	now     func() time.Time
	log     *slog.Logger
}

func New(store *ledger.Store, token string, secureCookie bool, log *slog.Logger) http.Handler {
	s := &server{
		store: store, digest: sha256.Sum256([]byte(token)),
		signKey: sha256.Sum256([]byte("llm-meter dashboard session\x00" + token)),
		secure:  secureCookie, now: func() time.Time { return time.Now().UTC() }, log: log,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/session", s.requireSession(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /api/clients", s.requireSession(s.listClients))
	mux.HandleFunc("POST /api/clients", s.requireSession(s.createClient))
	mux.HandleFunc("PATCH /api/clients/{client}", s.requireSession(s.renameClient))
	mux.HandleFunc("DELETE /api/clients/{client}", s.requireSession(s.archiveClient))
	mux.HandleFunc("POST /api/clients/{client}/keys", s.requireSession(s.createKey))
	mux.HandleFunc("DELETE /api/clients/{client}/keys/{key}", s.requireSession(s.revokeKey))
	mux.HandleFunc("GET /api/usage", s.requireSession(s.usage))
	mux.HandleFunc("GET /api/usage/requests", s.requireSession(s.usageRequests))
	protected := http.NewCrossOriginProtection().Handler(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		protected.ServeHTTP(w, r)
	})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &input) {
		return
	}
	provided := sha256.Sum256([]byte(input.Token))
	if !hmac.Equal(provided[:], s.digest[:]) {
		writeError(w, http.StatusUnauthorized, "Incorrect dashboard token")
		return
	}
	expires := s.now().Add(sessionDuration)
	payload := strconv.FormatInt(expires.Unix(), 10) + "." + rand.Text()
	signature := base64.RawURLEncoding.EncodeToString(s.sign(payload))
	http.SetCookie(w, s.cookie(payload+"."+signature, int(sessionDuration.Seconds())))
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) logout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, s.cookie("", -1))
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) cookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: cookieName, Value: value, Path: "/api/", MaxAge: maxAge,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode,
	}
}

func (s *server) sign(value string) []byte {
	mac := hmac.New(sha256.New, s.signKey[:])
	mac.Write([]byte(value))
	return mac.Sum(nil)
}

func (s *server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cookieName)
		if err != nil || !s.valid(cookie.Value) {
			writeError(w, http.StatusUnauthorized, "Unlock the dashboard to continue")
			return
		}
		next.ServeHTTP(w, r)
	}
}

func (s *server) valid(value string) bool {
	first, rest, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	nonce, signature, ok := strings.Cut(rest, ".")
	if !ok || len(nonce) != 26 || len(signature) != 43 {
		return false
	}
	expires, err := strconv.ParseInt(first, 10, 64)
	if err != nil || expires <= s.now().Unix() {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(signature)
	return err == nil && hmac.Equal(decoded, s.sign(first+"."+nonce))
}

func (s *server) listClients(w http.ResponseWriter, r *http.Request) {
	clients, err := s.store.Clients(r.Context())
	if err != nil {
		s.failed(w, "list clients", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": clients})
}

func (s *server) createClient(w http.ResponseWriter, r *http.Request) {
	name, ok := decodeClientName(w, r)
	if !ok {
		return
	}
	client, key, err := s.store.CreateClient(r.Context(), name, s.now())
	if err != nil {
		s.failed(w, "create client", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"client": client, "key": key})
}

func (s *server) renameClient(w http.ResponseWriter, r *http.Request) {
	name, ok := decodeClientName(w, r)
	if !ok {
		return
	}
	err := s.store.RenameClient(r.Context(), r.PathValue("client"), name)
	if ledger.NotFound(err) {
		writeError(w, http.StatusNotFound, "Client not found")
		return
	}
	if err != nil {
		s.failed(w, "rename client", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) archiveClient(w http.ResponseWriter, r *http.Request) {
	err := s.store.ArchiveClient(r.Context(), r.PathValue("client"), s.now())
	if ledger.NotFound(err) {
		writeError(w, http.StatusNotFound, "Client not found")
		return
	}
	if err != nil {
		s.failed(w, "archive client", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeClientName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var input struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &input) {
		return "", false
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		writeError(w, http.StatusBadRequest, "Enter a client name between 1 and 100 characters")
		return "", false
	}
	return name, true
}

func (s *server) createKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.store.CreateKey(r.Context(), r.PathValue("client"), s.now())
	if ledger.NotFound(err) {
		writeError(w, http.StatusNotFound, "Client not found")
		return
	}
	if err != nil {
		s.failed(w, "create client key", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key": key})
}

func (s *server) revokeKey(w http.ResponseWriter, r *http.Request) {
	err := s.store.RevokeKey(r.Context(), r.PathValue("client"), r.PathValue("key"), s.now())
	if ledger.NotFound(err) {
		writeError(w, http.StatusNotFound, "Client key not found")
		return
	}
	if err != nil {
		s.failed(w, "revoke client key", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) usage(w http.ResponseWriter, r *http.Request) {
	filter, err := usageFilter(r, s.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	data, err := s.store.Dashboard(r.Context(), filter)
	if err != nil {
		s.failed(w, "read dashboard usage", err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *server) usageRequests(w http.ResponseWriter, r *http.Request) {
	filter, err := usageFilter(r, s.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	data, err := s.store.Requests(r.Context(), filter)
	if err != nil {
		s.failed(w, "read dashboard requests", err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func usageFilter(r *http.Request, now time.Time) (ledger.Filter, error) {
	values := r.URL.Query()
	f := ledger.Filter{
		End: now, Interval: time.Hour,
		Sources: selectedValues(values["source"]), Clients: selectedValues(values["client"]), Models: selectedValues(values["model"]),
		Outcome: values.Get("outcome"),
	}
	switch values.Get("range") {
	case "", "24h":
		f.Start = now.Add(-24 * time.Hour)
	case "7d":
		f.Start = now.AddDate(0, 0, -7)
		f.Interval = 24 * time.Hour
	case "30d":
		f.Start = now.AddDate(0, 0, -30)
		f.Interval = 24 * time.Hour
	default:
		return f, errors.New("Use a 24-hour, 7-day or 30-day range")
	}
	if slices.ContainsFunc(f.Sources, func(source string) bool { return source != "api" && source != "codex" }) {
		return f, errors.New("Invalid usage source")
	}
	switch f.Outcome {
	case "", ledger.OutcomeSucceeded, ledger.OutcomeFailed:
	default:
		return f, errors.New("Invalid request outcome")
	}
	if slices.ContainsFunc(f.Clients, func(client string) bool { return len(client) > 100 }) ||
		slices.ContainsFunc(f.Models, func(model string) bool { return len(model) > 500 }) {
		return f, errors.New("Invalid client or model filter")
	}
	if value := values.Get("bucket"); value != "" {
		start, err := time.Parse(time.RFC3339, value)
		if err != nil || !start.Equal(start.Truncate(f.Interval)) {
			return f, errors.New("Invalid time bucket")
		}
		f.Bucket = &start
	}
	cursor, err := ledger.ParseCursor(values.Get("cursor"))
	f.Cursor = cursor
	return f, err
}

func selectedValues(values []string) []string {
	return slices.DeleteFunc(values, func(value string) bool { return value == "" })
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return false
	}
	return true
}

func (s *server) failed(w http.ResponseWriter, operation string, err error) {
	s.log.Error(operation, "error", err)
	writeError(w, http.StatusInternalServerError, "The dashboard could not complete the request")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

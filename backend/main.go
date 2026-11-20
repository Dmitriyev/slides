package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func errorResponse(w http.ResponseWriter, err error) {
	var api *apiError
	if errors.As(err, &api) {
		jsonResponse(w, api.status, map[string]string{"detail": api.detail})
		return
	}
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		jsonResponse(w, 413, map[string]string{"detail": "Request too large"})
		return
	}
	jsonResponse(w, 422, map[string]string{"detail": err.Error()})
}
func decode(r *http.Request, out any) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	kind := "rewrite"
	if _, ok := out.(*ChatRequest); ok {
		kind = "chat_request"
	}
	if err = validateWire(raw, kind); err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]any{"status": "ok", "generation_configured": os.Getenv("OPENROUTER_API_KEY") != ""})
	})
	mux.HandleFunc("POST /api/v1/presentations/generate", func(w http.ResponseWriter, r *http.Request) {
		result, err := s.create(r)
		if err != nil {
			errorResponse(w, err)
			return
		}
		jsonResponse(w, 200, result)
	})
	mux.HandleFunc("POST /api/v1/presentations/rewrite", func(w http.ResponseWriter, r *http.Request) {
		var req Rewrite
		if err := decode(r, &req); err != nil {
			errorResponse(w, err)
			return
		}
		if err := req.validate(); err != nil {
			errorResponse(w, err)
			return
		}
		deck, err := s.rewrite(r.Context(), req)
		if err != nil {
			errorResponse(w, err)
			return
		}
		jsonResponse(w, 200, deck)
	})
	mux.HandleFunc("POST /api/v1/presentations/chat", func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		if err := decode(r, &req); err != nil {
			errorResponse(w, err)
			return
		}
		if err := req.validate(); err != nil {
			errorResponse(w, err)
			return
		}
		result, err := s.chat(r.Context(), req)
		if err != nil {
			errorResponse(w, err)
			return
		}
		jsonResponse(w, 200, result)
	})
	origins := os.Getenv("CORS_ORIGINS")
	if origins == "" {
		origins = "http://localhost:5173,http://127.0.0.1:5173"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowed := false
		for _, o := range strings.Split(origins, ",") {
			if strings.TrimSpace(o) == origin && origin != "" {
				allowed = true
			}
		}
		if origin != "" {
			w.Header().Add("Vary", "Origin")
		}
		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		if r.Method == http.MethodOptions && origin != "" && r.Header.Get("Access-Control-Request-Method") != "" {
			method := r.Header.Get("Access-Control-Request-Method")
			valid := method == "POST" || method == "GET"
			for _, h := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
				h = strings.ToLower(strings.TrimSpace(h))
				if h != "" && h != "content-type" && h != "authorization" {
					valid = false
				}
			}
			if !allowed || !valid {
				jsonResponse(w, 400, map[string]string{"detail": "Disallowed CORS request"})
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "POST, GET")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(200)
			return
		}
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/") {
			token := os.Getenv("SLIDES_API_TOKEN")
			if token == "" {
				errorResponse(w, fail(503, "API token is not configured on server"))
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
				errorResponse(w, fail(401, "Invalid API token"))
				return
			}
			if r.ContentLength < 0 {
				errorResponse(w, fail(411, "Content-Length required"))
				return
			}
			if r.ContentLength > 21<<20 {
				errorResponse(w, fail(413, "Request too large"))
				return
			}
			select {
			case s.requests <- struct{}{}:
				defer func() { <-s.requests }()
			default:
				errorResponse(w, fail(503, "Too many requests. Please try again."))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 21<<20)
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *server) create(r *http.Request) (Deck, error) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			if r.MultipartForm != nil {
				r.MultipartForm.RemoveAll()
			}
			return Deck{}, err
		}
		defer r.MultipartForm.RemoveAll()
	} else {
		if err := r.ParseForm(); err != nil {
			return Deck{}, err
		}
	}
	description := r.FormValue("description")
	if !length(description, 0, 20000) {
		return Deck{}, fail(422, "Description: maximum 20,000 characters")
	}
	settings := defaultSettings()
	if raw := r.FormValue("settings"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &settings); err != nil {
			return Deck{}, fail(422, "Invalid settings")
		}
	}
	count := 6
	if raw := r.FormValue("slide_count"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 20 {
			return Deck{}, fail(422, "Invalid slide count")
		}
		count = n
	}
	source := strings.TrimSpace(description)
	total := 0
	if r.MultipartForm != nil {
		files := r.MultipartForm.File["files"]
		if len(files) > 8 {
			return Deck{}, fail(422, "Maximum 8 files")
		}
		for _, file := range files {
			f, err := file.Open()
			if err != nil {
				return Deck{}, err
			}
			data, err := io.ReadAll(io.LimitReader(f, int64(maxBytes-total+1)))
			f.Close()
			if err != nil {
				return Deck{}, err
			}
			total += len(data)
			if total > maxBytes {
				return Deck{}, fail(413, "Total file size: maximum 20 MB")
			}
			text, err := extract(r.Context(), file.Filename, data)
			if err != nil {
				return Deck{}, err
			}
			if strings.TrimSpace(text) == "" {
				return Deck{}, fail(422, fmt.Sprintf("%s: no text found. Scans require OCR; upload a document with selectable text.", file.Filename))
			}
			source += fmt.Sprintf("\n\n--- %s ---\n%s", file.Filename, text)
			if !length(source, 0, maxText) {
				return Deck{}, fail(413, "Too much text: maximum 100,000 characters")
			}
		}
	}
	if source == "" {
		return Deck{}, fail(422, "Add a description or a file")
	}
	var content Content
	err := s.generate(r.Context(), map[string]any{"task": fmt.Sprintf("Создай ровно %d слайдов", count), "source": source, "settings": settings, "schema": schemas["content"]}, "content", &content)
	if err != nil {
		return Deck{}, err
	}
	if len(content.Slides) != count {
		return Deck{}, fail(502, "The model returned an incorrect number of slides. Please generate again.")
	}
	for i := range content.Slides {
		sl := &content.Slides[i]
		sl.ID = newID()
		sl.Locked = false
		cleanChart(sl, source, settings)
	}
	return Deck{content, source}, nil
}
func main() {
	// Used by Docker health checks, so the runtime needs no Python or curl.
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		c := http.Client{Timeout: 4 * time.Second}
		resp, err := c.Get("http://127.0.0.1:8280/healthz")
		if err != nil {
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	srv := &http.Server{Addr: ":8280", Handler: newServer().handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 150 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	shutdownDone := make(chan struct{})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Print("Slide Tuner API listening on :8280")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-shutdownDone
}

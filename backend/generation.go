package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

//go:embed prompt.txt
var systemPrompt string

//go:embed schemas.json
var schemaData []byte
var schemas map[string]json.RawMessage

func init() {
	if err := json.Unmarshal(schemaData, &schemas); err != nil {
		panic(err)
	}
}

type Generator func(context.Context, map[string]any, string, any) error
type server struct {
	generate    Generator
	slots       chan struct{}
	requests    chan struct{}
	client      *http.Client
	providerURL string
}

func newServer() *server {
	s := &server{requests: make(chan struct{}, 24), slots: make(chan struct{}, 3), client: &http.Client{Timeout: 110 * time.Second}, providerURL: "https://openrouter.ai/api/v1/chat/completions"}
	s.generate = s.providerGenerate
	return s
}

type apiError struct {
	status int
	detail string
}

func (e *apiError) Error() string          { return e.detail }
func fail(status int, detail string) error { return &apiError{status, detail} }
func (s *server) providerGenerate(ctx context.Context, payload map[string]any, kind string, out any) error {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		return fail(503, "The generation API key is not configured on the server")
	}
	// The timeout includes waiting for a generation slot, and cancellation follows the caller.
	ctx, cancel := context.WithTimeout(ctx, 110*time.Second)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return fail(502, generationFailed)
	}
	user, err := json.Marshal(payload)
	if err != nil {
		return fail(502, generationFailed)
	}
	model := os.Getenv("SLIDES_MODEL")
	if model == "" {
		model = "google/gemini-3.8-flash"
	}
	body, err := json.Marshal(map[string]any{
		"model": model, "messages": []map[string]string{{"role": "system", "content": systemPrompt}, {"role": "user", "content": string(user)}},
		"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "presentation", "strict": true, "schema": schemas[kind]}}, "max_tokens": 14000,
	})
	if err != nil {
		return fail(502, generationFailed)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.providerURL, bytes.NewReader(body))
	if err != nil {
		return fail(502, generationFailed)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return fail(502, generationFailed)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fail(502, generationFailed)
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || json.Unmarshal(raw, &envelope) != nil || len(envelope.Choices) == 0 {
		return fail(502, generationFailed)
	}
	if validateWire([]byte(envelope.Choices[0].Message.Content), kind) != nil || json.Unmarshal([]byte(envelope.Choices[0].Message.Content), out) != nil {
		return fail(502, generationFailed)
	}
	switch v := out.(type) {
	case *Content:
		err = v.validate()
	case *ChatAnswer:
		if !length(v.Message, 1, 20000) {
			err = fmt.Errorf("Invalid answer")
		}
		if v.Content != nil {
			err = v.Content.validate()
		}
	}
	if err != nil {
		return fail(502, generationFailed)
	}
	return nil
}

var numbers = regexp.MustCompile(`[+-]?[0-9]+(?:[.,][0-9]+)?`)
var numericRange = regexp.MustCompile(`[0-9]\s*[–—-]\s*[0-9]`)

func cleanChart(slide *Slide, source string, settings Settings) {
	if settings.Charts == 0 {
		slide.Chart = nil
	}
	if slide.Chart != nil {
		for _, d := range slide.Chart.Data {
			verified := false
			for _, loc := range numbers.FindAllStringIndex(d.Evidence, -1) {
				if loc[0] > 0 {
					r, _ := utf8.DecodeLastRuneInString(d.Evidence[:loc[0]])
					if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' {
						continue
					}
				}
				n, err := strconv.ParseFloat(strings.ReplaceAll(d.Evidence[loc[0]:loc[1]], ",", "."), 64)
				if err == nil && n == d.Value {
					verified = true
				}
			}
			if d.Value < 0 || numericRange.MatchString(d.Evidence) || !strings.Contains(source, d.Evidence) || !verified {
				slide.Chart = nil
				slide.Note = "Chart hidden: the values could not be verified against the source material."
				break
			}
		}
	}
	slide.Settings = settings
}

func (s *server) rewrite(ctx context.Context, r Rewrite) (Deck, error) {
	deck := r.Deck
	originals := []Slide{}
	for _, sl := range deck.Slides {
		if r.Scope == "deck" || (r.SlideID != nil && sl.ID == *r.SlideID) {
			originals = append(originals, sl)
		}
	}
	var content Content
	err := s.generate(ctx, map[string]any{"task": "Переработай эти слайды в том же порядке, без добавления или удаления слайдов. Для locked=true меняй только chart/note, title и paragraphs сохраняй.", "source": deck.Source, "slides": originals, "settings": r.Settings, "schema": schemas["content"]}, "content", &content)
	if err != nil {
		return Deck{}, err
	}
	if len(content.Slides) != len(originals) {
		return Deck{}, fail(502, "The model changed the number of slides. Your current version has been preserved.")
	}
	changes := map[string]Slide{}
	for i, old := range originals {
		next := content.Slides[i]
		next.ID = old.ID
		next.Locked = old.Locked
		if old.Locked {
			next.Title = old.Title
			next.Paragraphs = old.Paragraphs
		}
		cleanChart(&next, deck.Source, r.Settings)
		if old.Locked {
			next.Settings = old.Settings
			next.Settings.Charts = r.Settings.Charts
		}
		changes[old.ID] = next
	}
	deck.Slides = append([]Slide(nil), deck.Slides...)
	for i, sl := range deck.Slides {
		if next, ok := changes[sl.ID]; ok {
			deck.Slides[i] = next
		}
	}
	return deck, nil
}

func (s *server) chat(ctx context.Context, r ChatRequest) (ChatResult, error) {
	deck := r.Deck
	originals := map[string]Slide{}
	for _, sl := range deck.Slides {
		originals[sl.ID] = sl
	}
	var answer ChatAnswer
	err := s.generate(ctx, map[string]any{
		"task":   "Ответь пользователю и выполни его правки. Для вопроса без правок верни content=null. Ты видишь текущие слайды, исходники, историю диалога и альтернативные тексты с ключами. Можешь использовать альтернативы по просьбе пользователя. При правках верни полное новое content для указанной области scope. Для scope=slide верни ровно один слайд с исходным id. Для scope=deck можно добавлять, удалять и переставлять слайды; сохраняй id существующих. Не удаляй locked-слайды, сохраняй их title и paragraphs. Меняй settings только по просьбе. Не вставляй служебную информацию и альтернативы в текст слайдов.",
		"source": deck.Source, "current": deck.Content, "scope": r.Scope, "slide_id": r.SlideID, "settings": r.Settings, "alternatives": r.Alternatives, "messages": r.Messages, "message": r.Message,
	}, "chat", &answer)
	if err != nil {
		return ChatResult{}, err
	}
	if answer.Content == nil {
		return ChatResult{answer.Message, deck, false}, nil
	}
	seen := map[string]bool{}
	for _, sl := range answer.Content.Slides {
		if seen[sl.ID] {
			return ChatResult{}, fail(502, "The model returned duplicate slide IDs. Your current version has been preserved.")
		}
		seen[sl.ID] = true
	}
	if r.Scope == "slide" {
		if len(answer.Content.Slides) != 1 || answer.Content.Slides[0].ID != *r.SlideID {
			return ChatResult{}, fail(502, "The model changed the selected scope. Your current version has been preserved.")
		}
	} else {
		for _, old := range deck.Slides {
			if old.Locked && !seen[old.ID] {
				return ChatResult{}, fail(502, "The model removed a locked slide. Your current version has been preserved.")
			}
		}
	}
	changed := append([]Slide(nil), answer.Content.Slides...)
	for i := range changed {
		next := &changed[i]
		old, ok := originals[next.ID]
		next.Locked = ok && old.Locked
		if next.Locked {
			next.Title = old.Title
			next.Paragraphs = old.Paragraphs
			charts := next.Settings.Charts
			next.Settings = old.Settings
			next.Settings.Charts = charts
		}
		cleanChart(next, deck.Source, next.Settings)
	}
	if r.Scope == "slide" {
		deck.Slides = append([]Slide(nil), deck.Slides...)
		for i, sl := range deck.Slides {
			if sl.ID == *r.SlideID {
				deck.Slides[i] = changed[0]
			}
		}
	} else {
		deck.Title = answer.Content.Title
		deck.Slides = changed
	}
	before, _ := json.Marshal(r.Deck)
	after, _ := json.Marshal(deck)
	return ChatResult{answer.Message, deck, !bytes.Equal(before, after)}, nil
}

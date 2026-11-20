package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

const maxText = 100_000
const maxBytes = 20 << 20
const generationFailed = "Generation failed. Please try again; your current version has been preserved."

type Settings struct {
	Detail     int `json:"detail"`
	Complexity int `json:"complexity"`
	Tone       int `json:"tone"`
	Charts     int `json:"charts"`
}

func defaultSettings() Settings { return Settings{1, 1, 1, 1} }
func (s *Settings) UnmarshalJSON(b []byte) error {
	type plain Settings
	v := plain(defaultSettings())
	if strings.TrimSpace(string(b)) == "null" {
		return fmt.Errorf("Invalid settings")
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*s = Settings(v)
	return s.validate()
}
func (s Settings) validate() error {
	for _, v := range []int{s.Detail, s.Complexity, s.Tone, s.Charts} {
		if v < 0 || v > 2 {
			return fmt.Errorf("Invalid settings")
		}
	}
	return nil
}

type Datum struct {
	Label    string  `json:"label"`
	Value    float64 `json:"value"`
	Evidence string  `json:"evidence"`
}
type Chart struct {
	Title string  `json:"title"`
	Unit  string  `json:"unit"`
	Data  []Datum `json:"data"`
}
type Slide struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Paragraphs []string `json:"paragraphs"`
	Chart      *Chart   `json:"chart"`
	Note       string   `json:"note"`
	Locked     bool     `json:"locked"`
	Settings   Settings `json:"settings"`
}

func newID() string {
	b := randBytes()
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func randBytes() [16]byte {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return b
}
func (s *Slide) UnmarshalJSON(b []byte) error {
	type plain Slide
	v := plain{ID: newID(), Paragraphs: []string{}, Settings: defaultSettings()}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*s = Slide(v)
	return nil
}

type Content struct {
	Title  string  `json:"title"`
	Slides []Slide `json:"slides"`
}
type Deck struct {
	Content
	Source string `json:"source"`
}
type Rewrite struct {
	Deck     Deck     `json:"deck"`
	Scope    string   `json:"scope"`
	SlideID  *string  `json:"slide_id"`
	Settings Settings `json:"settings"`
}
type ChatMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}
type Alternative struct {
	Key      string   `json:"key"`
	Scope    string   `json:"scope"`
	SlideID  *string  `json:"slide_id"`
	Settings Settings `json:"settings"`
	Content  Content  `json:"content"`
}
type ChatRequest struct {
	Rewrite
	Message      string        `json:"message"`
	Messages     []ChatMessage `json:"messages"`
	Alternatives []Alternative `json:"alternatives"`
}
type ChatAnswer struct {
	Message string   `json:"message"`
	Content *Content `json:"content"`
}
type ChatResult struct {
	Message string `json:"message"`
	Deck    Deck   `json:"deck"`
	Changed bool   `json:"changed"`
}

func length(s string, min, max int) bool { n := utf8.RuneCountInString(s); return n >= min && n <= max }
func (c Content) validate() error {
	if !length(c.Title, 1, 300) || len(c.Slides) < 1 || len(c.Slides) > 20 {
		return fmt.Errorf("Invalid presentation")
	}
	for _, s := range c.Slides {
		if !length(s.ID, 0, 100) || !length(s.Title, 1, 300) || len(s.Paragraphs) > 12 || !length(s.Note, 0, 1000) || s.Settings.validate() != nil {
			return fmt.Errorf("Invalid slide")
		}
		if s.Chart == nil {
			continue
		}
		ch := s.Chart
		if !length(ch.Title, 0, 200) || !length(ch.Unit, 0, 50) || len(ch.Data) < 2 || len(ch.Data) > 12 {
			return fmt.Errorf("Invalid chart")
		}
		for _, d := range ch.Data {
			if !length(d.Label, 0, 200) || !length(d.Evidence, 1, 500) || math.IsNaN(d.Value) || math.IsInf(d.Value, 0) {
				return fmt.Errorf("Invalid chart data")
			}
		}
	}
	return nil
}
func (d Deck) validate() error {
	if !length(d.Source, 1, maxText) {
		return fmt.Errorf("Invalid source")
	}
	return d.Content.validate()
}
func validScope(s string) bool { return s == "slide" || s == "deck" }
func (r Rewrite) validate() error {
	if err := r.Deck.validate(); err != nil {
		return err
	}
	if !validScope(r.Scope) {
		return fmt.Errorf("Invalid scope")
	}
	if err := r.Settings.validate(); err != nil {
		return err
	}
	if r.Scope == "slide" {
		for _, s := range r.Deck.Slides {
			if r.SlideID != nil && s.ID == *r.SlideID {
				return nil
			}
		}
		return fmt.Errorf("Slide not found")
	}
	return nil
}
func (r ChatRequest) validate() error {
	if err := r.Rewrite.validate(); err != nil {
		return err
	}
	if !length(r.Message, 1, 20000) || len(r.Messages) > 20 || len(r.Alternatives) > 18 {
		return fmt.Errorf("Invalid chat request")
	}
	for _, m := range r.Messages {
		if (m.Role != "user" && m.Role != "assistant") || !length(m.Text, 1, 20000) {
			return fmt.Errorf("Invalid chat message")
		}
	}
	for _, a := range r.Alternatives {
		if !length(a.Key, 0, 200) || !validScope(a.Scope) || a.Settings.validate() != nil || a.Content.validate() != nil {
			return fmt.Errorf("Invalid alternative")
		}
	}
	return nil
}

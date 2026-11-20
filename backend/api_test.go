package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureDeck() Deck {
	return Deck{Content{"Deck", []Slide{
		{ID: "a", Title: "Manual", Paragraphs: []string{"Original"}, Locked: true, Settings: defaultSettings()},
		{ID: "b", Title: "Untouched", Paragraphs: []string{}, Settings: defaultSettings()},
	}}, "Material"}
}
func mockServer(t *testing.T, content Content) *server {
	t.Helper()
	t.Setenv("SLIDES_API_TOKEN", "test-secret")
	s := newServer()
	s.generate = func(_ context.Context, _ map[string]any, _ string, out any) error {
		b, _ := json.Marshal(content)
		return json.Unmarshal(b, out)
	}
	return s
}
func request(s *server, path string, payload any) *httptest.ResponseRecorder {
	body, _ := json.Marshal(payload)
	r := httptest.NewRequest("POST", "/api/v1/presentations/"+path, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer test-secret")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	return w
}
func readResponse[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func TestAdmission(t *testing.T) {
	s := newServer()
	cases := []struct {
		name, token, auth string
		length            int64
		want              int
	}{
		{"no server token", "", "", 2, 503}, {"no auth", "test-secret", "", 2, 401}, {"wrong token", "test-secret", "Bearer invalid", 2, 401},
		{"no length", "test-secret", "Bearer test-secret", -1, 411}, {"body limit", "test-secret", "Bearer test-secret", 22 << 20, 413},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SLIDES_API_TOKEN", tc.token)
			r := httptest.NewRequest("POST", "/api/v1/presentations/rewrite", strings.NewReader("{}"))
			r.ContentLength = tc.length
			r.Header.Set("Authorization", tc.auth)
			w := httptest.NewRecorder()
			s.handler().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d", w.Code)
			}
		})
	}
}
func TestCORSAndHealth(t *testing.T) {
	s := newServer()
	t.Setenv("CORS_ORIGINS", "http://localhost:5173")
	t.Setenv("OPENROUTER_API_KEY", "")
	for _, origin := range []string{"http://localhost:5173", "https://invalid.example"} {
		r := httptest.NewRequest("OPTIONS", "/api/v1/presentations/chat", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, r)
		if origin == "http://localhost:5173" {
			if w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != origin {
				t.Fatal(w)
			}
		} else if w.Code != 400 {
			t.Fatal(w)
		}
	}
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	v := readResponse[map[string]any](t, w)
	if v["generation_configured"] != false {
		t.Fatal(v)
	}
}

func TestRequestCapacity(t *testing.T) {
	s := mockServer(t, Content{})
	for i := 0; i < cap(s.requests); i++ {
		s.requests <- struct{}{}
	}
	if w := request(s, "rewrite", map[string]any{}); w.Code != 503 {
		t.Fatal(w.Code)
	}
	// Health remains available while generation requests are saturated.
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
func TestValidation(t *testing.T) {
	s := mockServer(t, fixtureDeck().Content)
	id := "a"
	valid := Rewrite{fixtureDeck(), "slide", &id, defaultSettings()}
	raw, _ := json.Marshal(valid)
	var obj map[string]any
	_ = json.Unmarshal(raw, &obj)
	for _, change := range []func(map[string]any){
		func(v map[string]any) { delete(v, "settings") }, func(v map[string]any) { v["settings"] = nil }, func(v map[string]any) { v["settings"] = map[string]any{"tone": 99} },
		func(v map[string]any) { v["settings"] = map[string]any{"tone": 1.5} }, func(v map[string]any) { v["settings"] = map[string]any{"tone": nil} },
		func(v map[string]any) { v["scope"] = "invalid" }, func(v map[string]any) { v["slide_id"] = "missing" },
		func(v map[string]any) { v["deck"].(map[string]any)["source"] = "" }, func(v map[string]any) { v["scope"] = map[string]any{} },
	} {
		var v map[string]any
		_ = json.Unmarshal(raw, &v)
		change(v)
		w := request(s, "rewrite", v)
		if w.Code != 422 {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
	}
}
func TestRewriteLockScopeAndAtomicity(t *testing.T) {
	generated := Content{"Model title", []Slide{{Title: "Changed", Paragraphs: []string{"Changed"}, Settings: defaultSettings()}}}
	s := mockServer(t, generated)
	id := "a"
	deck := fixtureDeck()
	settings := Settings{2, 2, 2, 0}
	result := readResponse[Deck](t, request(s, "rewrite", Rewrite{deck, "slide", &id, settings}))
	if result.Slides[0].Title != "Manual" || !reflect.DeepEqual(result.Slides[0].Paragraphs, deck.Slides[0].Paragraphs) || result.Slides[0].Settings.Detail != 1 || result.Slides[0].Settings.Charts != 0 || !reflect.DeepEqual(result.Slides[1], deck.Slides[1]) {
		t.Fatal(result)
	}
	// Wrong slide count fails before replacing any part of a deck.
	if w := request(s, "rewrite", Rewrite{deck, "deck", nil, settings}); w.Code != 502 {
		t.Fatal(w.Code)
	}
	generated.Slides = append(generated.Slides, generated.Slides[0])
	s = mockServer(t, generated)
	result = readResponse[Deck](t, request(s, "rewrite", Rewrite{deck, "deck", nil, settings}))
	if result.Slides[1].ID != "b" || result.Slides[1].Settings != settings || result.Title != "Deck" {
		t.Fatal(result)
	}
	if deck.Slides[0].Title != "Manual" || deck.Slides[1].Title != "Untouched" {
		t.Fatal("mutated original")
	}
}
func chatFixture() ChatRequest {
	id := "a"
	return ChatRequest{Rewrite: Rewrite{fixtureDeck(), "slide", &id, defaultSettings()}, Message: "Use the brief alternative", Messages: []ChatMessage{{"user", "Remember the audience"}}, Alternatives: []Alternative{{Key: "brief", Scope: "slide", SlideID: &id, Settings: defaultSettings(), Content: Content{"Deck", []Slide{{ID: "a", Title: "Brief alternative", Paragraphs: []string{}, Settings: defaultSettings()}}}}}}
}
func TestChatContextAndQuestion(t *testing.T) {
	s := mockServer(t, Content{})
	req := chatFixture()
	s.generate = func(_ context.Context, p map[string]any, kind string, out any) error {
		if kind != "chat" || p["source"] != "Material" || !reflect.DeepEqual(p["alternatives"], req.Alternatives) || !reflect.DeepEqual(p["messages"], req.Messages) {
			t.Fatal("lost context", p)
		}
		*out.(*ChatAnswer) = ChatAnswer{Message: "Here is the answer"}
		return nil
	}
	result := readResponse[ChatResult](t, request(s, "chat", req))
	if result.Changed || !reflect.DeepEqual(result.Deck, req.Deck) {
		t.Fatal(result)
	}
}
func TestChatEdits(t *testing.T) {
	cases := []struct {
		scope string
		ids   []string
		want  int
	}{
		{"slide", []string{"b"}, 502}, {"slide", []string{"a", "b"}, 502}, {"deck", []string{"b"}, 502}, {"deck", []string{"a", "a"}, 502},
		{"slide", []string{"a"}, 200}, {"deck", []string{"b", "new", "a"}, 200},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.scope, tc.ids), func(t *testing.T) {
			s := mockServer(t, Content{})
			s.generate = func(_ context.Context, _ map[string]any, _ string, out any) error {
				content := Content{Title: "New deck"}
				for _, id := range tc.ids {
					content.Slides = append(content.Slides, Slide{ID: id, Title: "Changed", Paragraphs: []string{"Changed"}, Settings: Settings{2, 2, 2, 1}, Locked: true})
				}
				*out.(*ChatAnswer) = ChatAnswer{"Updated", &content}
				return nil
			}
			req := chatFixture()
			req.Scope = tc.scope
			w := request(s, "chat", req)
			if w.Code != tc.want {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if tc.want == 200 {
				result := readResponse[ChatResult](t, w)
				for _, sl := range result.Deck.Slides {
					if sl.ID == "a" && (sl.Title != "Manual" || sl.Settings.Detail != 1 || !sl.Locked) {
						t.Fatal(sl)
					}
					if sl.ID == "new" && sl.Locked {
						t.Fatal("new slide locked by model")
					}
				}
			}
		})
	}
}
func TestCharts(t *testing.T) {
	for _, tc := range []struct {
		evidence, source string
		value            float64
		charts           int
		valid            bool
	}{
		{"B: 440", "B: 440", 440, 1, true}, {"B: 440", "B: 440", 44, 1, false}, {"B: 42", "B: 440", 42, 1, false},
		{"B: 42-50", "B: 42-50", 42, 1, false}, {"B: 42–50", "B: 42–50", 42, 1, false}, {"B: -42", "B: -42", -42, 1, false},
		{"B: 4,2", "B: 4,2", 4.2, 1, true}, {"B: 42", "B: 42", 42, 0, false}, {"B: x42", "B: x42", 42, 1, false},
	} {
		sl := Slide{Chart: &Chart{Data: []Datum{{"B", tc.value, tc.evidence}}}}
		settings := defaultSettings()
		settings.Charts = tc.charts
		cleanChart(&sl, tc.source, settings)
		if (sl.Chart != nil) != tc.valid {
			t.Fatal(tc)
		}
	}
}

func multipartRequest(s *server, fields map[string]string, files map[string][]byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	m := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = m.WriteField(k, v)
	}
	for name, data := range files {
		part, _ := m.CreateFormFile("files", name)
		_, _ = part.Write(data)
	}
	_ = m.Close()
	r := httptest.NewRequest("POST", "/api/v1/presentations/generate", &body)
	r.Header.Set("Content-Type", m.FormDataContentType())
	r.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	return w
}
func TestGenerate(t *testing.T) {
	s := mockServer(t, Content{"Deck", []Slide{{Title: "Generated", Paragraphs: []string{}, Settings: defaultSettings()}}})
	result := readResponse[Deck](t, multipartRequest(s, map[string]string{"slide_count": "1"}, map[string][]byte{"source.txt": []byte("\xEF\xBB\xBFRevenue 42")}))
	if !strings.HasSuffix(result.Source, "Revenue 42") || result.Slides[0].ID == "" || result.Slides[0].Locked {
		t.Fatal(result)
	}
	for _, tc := range []struct {
		fields map[string]string
		files  map[string][]byte
		want   int
	}{
		{nil, nil, 422}, {map[string]string{"description": "Hello", "settings": "{\"detail\":99}"}, nil, 422},
		{map[string]string{"slide_count": "0"}, nil, 422}, {nil, map[string][]byte{"bad.exe": []byte("data")}, 415},
		{nil, map[string][]byte{"empty.txt": {}}, 422}, {nil, map[string][]byte{"bad.txt": {255}}, 422},
		{nil, map[string][]byte{"source.txt": bytes.Repeat([]byte("x"), maxText+1)}, 413},
		{map[string]string{"slide_count": "2", "description": "Hello"}, nil, 502},
		{nil, map[string][]byte{"source.txt": bytes.Repeat([]byte("x"), maxBytes+1)}, 413},
	} {
		if w := multipartRequest(s, tc.fields, tc.files); w.Code != tc.want {
			t.Fatalf("got %d want %d: %s", w.Code, tc.want, w.Body)
		}
	}
	form := url.Values{"description": {"Hello"}, "slide_count": {"1"}}.Encode()
	r := httptest.NewRequest("POST", "/api/v1/presentations/generate", strings.NewReader(form))
	r.Header.Set("Authorization", "Bearer test-secret")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	_ = readResponse[Deck](t, w)
}
func zipped(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, text := range parts {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, text)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestOfficeExtraction(t *testing.T) {
	data := zipped(t, map[string]string{"word/document.xml": `<w:document xmlns:w="urn:word"><w:body><w:p><w:r><w:t>Hello </w:t></w:r><w:r><w:t>world</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>A &amp; B</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`})
	text, err := extract(context.Background(), "test.docx", data)
	if err != nil || !strings.Contains(text, "Hello world") || !strings.Contains(text, "A & B") {
		t.Fatal(text, err)
	}
	data = zipped(t, map[string]string{
		"ppt/presentation.xml":            `<p:presentation xmlns:p="urn:p" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:sldIdLst><p:sldId id="256" r:id="rId2"/><p:sldId id="257" r:id="rId1"/></p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<Relationships><Relationship Id="rId1" Target="slides/slide1.xml"/><Relationship Id="rId2" Target="slides/slide10.xml"/></Relationships>`,
		"ppt/slides/slide1.xml":           `<slide><p><t>Second</t></p></slide>`, "ppt/slides/slide10.xml": `<slide><p><t>First</t></p></slide>`,
	})
	text, err = extract(context.Background(), "test.pptx", data)
	if err != nil || text != "First\nSecond" {
		t.Fatal(text, err)
	}
	if _, err := extract(context.Background(), "bad.docx", []byte("bad")); err == nil {
		t.Fatal("accepted invalid zip")
	}
}
func pdfFixture(pages int, text string) []byte {
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", fmt.Sprintf("<< /Type /Pages /Kids [3 0 R] /Count %d >>", pages), "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	stream := "BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET"
	objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, off := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return b.Bytes()
}
func TestPDFExtraction(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("poppler-utils is required for PDF extraction")
	}
	text, err := extract(context.Background(), "source.pdf", pdfFixture(1, "Revenue 42"))
	if err != nil || !strings.Contains(text, "Revenue 42") {
		t.Fatal(text, err)
	}
	if _, err := extract(context.Background(), "large.pdf", pdfFixture(101, "Hello")); err == nil {
		t.Fatal("accepted >100 pages")
	}
	if _, err := extract(context.Background(), "bad.pdf", []byte("invalid")); err == nil {
		t.Fatal("accepted invalid PDF")
	}
}
func TestProviderContractAndFailures(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "provider-secret")
	s := newServer()
	for _, tc := range []struct {
		status int
		reply  string
		want   int
	}{
		{200, `{"title":"Deck","slides":[{"title":"Generated"}]}`, 0},
		{200, `{"message":"Answer","content":null}`, 0},
		{200, `{"title":"Deck","slides":[{"title":"Generated","paragraphs":null}]}`, 502},
		{200, `{"title":"Deck","slides":[]}`, 502}, {200, `{"title":"Deck","slides":[{}]}`, 502},
		{200, `not json`, 502}, {429, `{}`, 502},
	} {
		func() {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer provider-secret" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("invalid provider headers")
				}
				var payload map[string]any
				if json.NewDecoder(r.Body).Decode(&payload) != nil {
					t.Error("invalid payload")
				}
				schema, _ := json.Marshal(payload["response_format"])
				if bytes.Contains(schema, []byte(`"minLength"`)) || bytes.Contains(schema, []byte(`"maximum"`)) {
					t.Error("provider schema includes rejected bounds")
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": tc.reply}}}})
			}))
			defer provider.Close()
			s.providerURL = provider.URL
			var out any = &Content{}
			kind := "content"
			if strings.Contains(tc.reply, `"message"`) {
				out = &ChatAnswer{}
				kind = "chat"
			}
			err := s.providerGenerate(context.Background(), map[string]any{"source": "Material"}, kind, out)
			if tc.want == 0 && err != nil {
				t.Fatal(err)
			}
			if tc.want != 0 {
				var api *apiError
				if !errors.As(err, &api) || api.status != tc.want {
					t.Fatal(err)
				}
			}
		}()
	}
	t.Setenv("OPENROUTER_API_KEY", "")
	if err := s.providerGenerate(context.Background(), nil, "content", &Content{}); err == nil {
		t.Fatal("missing key accepted")
	}
}
func TestProviderConcurrencyAndCancellation(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test")
	s := newServer()
	var active, peak atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"title":"Deck","slides":[{"title":"Slide"}]}`}}}})
	}))
	defer provider.Close()
	s.providerURL = provider.URL
	var wg sync.WaitGroup
	for i := 0; i < 9; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.providerGenerate(context.Background(), nil, "content", &Content{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if peak.Load() > 3 || peak.Load() < 2 {
		t.Fatal("invalid concurrency", peak.Load())
	}
	for i := 0; i < 3; i++ {
		s.slots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.providerGenerate(ctx, nil, "content", &Content{}); err == nil {
		t.Fatal("canceled queued request succeeded")
	}
}

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Stop external extractors before a malformed document can fill memory or /tmp.
type cappedBuffer struct {
	bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.max-b.Len() {
		return 0, fmt.Errorf("Extracted document is too large")
	}
	return b.Buffer.Write(p)
}
func commandOutput(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out := &cappedBuffer{max: 4 * maxText}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	return out.String(), err
}
func extract(ctx context.Context, name string, data []byte) (string, error) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	switch ext {
	case "txt", "md", "csv", "pdf", "docx", "pptx":
	default:
		return "", fail(415, fmt.Sprintf("The %s format is not supported yet", ext))
	}
	text, err := extractDocument(ctx, ext, data)
	if err != nil {
		return "", fail(422, fmt.Sprintf("Could not read %s. Check the file and make sure it contains selectable text.", name))
	}
	return text, nil
}
func extractDocument(ctx context.Context, ext string, data []byte) (string, error) {
	if ext == "txt" || ext == "md" || ext == "csv" {
		if !utf8.Valid(data) {
			return "", fmt.Errorf("Invalid UTF-8")
		}
		return strings.TrimPrefix(string(data), "\uFEFF"), nil
	}
	if ext == "pdf" {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		f, err := os.CreateTemp("", "slides-*.pdf")
		if err != nil {
			return "", err
		}
		defer os.Remove(f.Name())
		if _, err = f.Write(data); err != nil {
			f.Close()
			return "", err
		}
		if err = f.Close(); err != nil {
			return "", err
		}
		info, err := commandOutput(ctx, "pdfinfo", f.Name())
		if err != nil {
			return "", err
		}
		pages := 0
		for _, line := range strings.Split(info, "\n") {
			if strings.HasPrefix(line, "Pages:") {
				pages, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Pages:")))
			}
		}
		if pages < 1 || pages > 100 {
			return "", fmt.Errorf("PDF: maximum 100 pages")
		}
		return commandOutput(ctx, "pdftotext", "-enc", "UTF-8", f.Name(), "-")
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var total uint64
	parts := map[string]*zip.File{}
	for _, f := range archive.File {
		if f.UncompressedSize64 > 50<<20 || total > 50<<20-f.UncompressedSize64 {
			return "", fmt.Errorf("Uncompressed document is too large")
		}
		total += f.UncompressedSize64
		parts[f.Name] = f
	}
	readPart := func(name string) ([]byte, error) {
		f := parts[name]
		if f == nil {
			return nil, fmt.Errorf("Missing %s", name)
		}
		r, e := f.Open()
		if e != nil {
			return nil, e
		}
		defer r.Close()
		b, e := io.ReadAll(io.LimitReader(r, 50<<20+1))
		if len(b) > 50<<20 {
			return nil, fmt.Errorf("Document too large")
		}
		return b, e
	}
	if ext == "docx" {
		b, err := readPart("word/document.xml")
		if err != nil {
			return "", err
		}
		return officeText(b, true)
	}
	// Use the presentation relationship order, not ZIP order or slide10 before slide2.
	presentation, err := readPart("ppt/presentation.xml")
	if err != nil {
		return "", err
	}
	rels, err := readPart("ppt/_rels/presentation.xml.rels")
	if err != nil {
		return "", err
	}
	var relationships struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
			Mode   string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	if err = xml.Unmarshal(rels, &relationships); err != nil {
		return "", err
	}
	targets := map[string]string{}
	for _, r := range relationships.Items {
		if r.Mode != "External" {
			target := filepath.ToSlash(filepath.Clean("ppt/" + r.Target))
			if strings.HasPrefix(r.Target, "/") {
				target = strings.TrimPrefix(r.Target, "/")
			}
			targets[r.ID] = target
		}
	}
	dec := xml.NewDecoder(bytes.NewReader(presentation))
	names := []string{}
	for {
		t, e := dec.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
		if start, ok := t.(xml.StartElement); ok && start.Name.Local == "sldId" {
			for _, a := range start.Attr {
				if a.Name.Local == "id" && strings.Contains(a.Name.Space, "relationships") {
					names = append(names, targets[a.Value])
				}
			}
		}
	}
	texts := []string{}
	for _, name := range names {
		b, err := readPart(name)
		if err != nil {
			return "", err
		}
		t, err := officeText(b, false)
		if err != nil {
			return "", err
		}
		texts = append(texts, t)
	}
	return strings.Join(texts, "\n"), nil
}

func officeText(data []byte, word bool) (string, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	var out strings.Builder
	depth := 0
	inText := false
	skip := 0
	for {
		t, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch v := t.(type) {
		case xml.StartElement:
			depth++
			if word && v.Name.Local == "del" {
				skip = depth
			}
			if skip > 0 {
				continue
			}
			switch v.Name.Local {
			case "t":
				inText = true
			case "tab":
				out.WriteString("\t")
			case "br":
				out.WriteString("\n")
			}
		case xml.CharData:
			if inText && skip == 0 {
				out.Write(v)
			}
		case xml.EndElement:
			if skip == depth {
				skip = 0
			}
			if skip == 0 {
				switch v.Name.Local {
				case "t":
					inText = false
				case "p":
					out.WriteByte('\n')
				case "tc":
					if word {
						out.WriteString(" | ")
					}
				}
			}
			depth--
		}
		if out.Len() > 4*maxText {
			return "", fmt.Errorf("Extracted text too large")
		}
	}
	return strings.TrimSpace(out.String()), nil
}

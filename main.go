package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	emoji "github.com/yuin/goldmark-emoji"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	meta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"go.abhg.dev/goldmark/mermaid"
)

type NavItem struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type Config struct {
	Nav    []NavItem `json:"nav"`
	Footer string    `json:"footer"`
}

type TemplateData struct {
	Title  string
	Body   template.HTML
	Error  string
	Config *Config
}

type PageInfo struct {
	Title string
	Path  string
	Slug  string
}

func LoadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var config Config
	if err := json.NewDecoder(file).Decode(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

var gm = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
		&mermaid.Extender{},
		extension.Typographer,
		extension.Footnote,
		extension.DefinitionList,
		meta.Meta,
		highlighting.NewHighlighting(
			highlighting.WithStyle("github"),
			highlighting.WithGuessLanguage(true),
		),
		emoji.Emoji,
	),
	goldmark.WithRendererOptions(
		html.WithHardWraps(),
		html.WithUnsafe(),
	),
)

func renderMarkdown(path string) (string, error) {
	context := parser.NewContext()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := gm.Convert(data, &buf, parser.WithContext(context)); err != nil {
		return "", fmt.Errorf("failed to convert markdown: %w", err)
	}

	return buf.String(), nil
}

func parseMetadata(path string) (map[string]any, error) {
	context := parser.NewContext()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	node := gm.Parser().Parse(text.NewReader(data), parser.WithContext(context))
	if node == nil {
		return nil, fmt.Errorf("failed to parse markdown from %s", path)
	}
	return meta.Get(context), nil
}

func parseDir(pageDir string) (map[string]PageInfo, error) {
	pageMap := make(map[string]PageInfo)

	pages, err := filepath.Glob(filepath.Join(pageDir, "*.md"))
	if err != nil || len(pages) == 0 {
		return nil, fmt.Errorf("could not find markdown files: %w", err)
	}

	for _, page := range pages {
		metaData, err := parseMetadata(page)
		if err != nil {
			return nil, fmt.Errorf("could not parse metadata for %s: %w", page, err)
		}

		title, exists := metaData["title"].(string)
		if !exists {
			title = filepath.Base(page)
		}

		base := strings.TrimSpace(filepath.Base(page))
		id := strings.ReplaceAll(strings.TrimSuffix(base, filepath.Ext(base)), " ", "_")
		pageMap[id] = PageInfo{
			Title: title,
			Path:  page,
			Slug:  id,
		}
	}

	return pageMap, nil
}

func isDir(path string) (bool, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return fileInfo.IsDir(), nil
}

func main() {
	config, err := LoadConfig("config.json")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	tmpl := template.Must(template.ParseFiles(
		"static/templates/base.html",
		"static/templates/header.html",
		"static/templates/footer.html",
		"static/templates/policy.html",
	))

	pages, err := parseDir(filepath.Join("static", "pages"))
	if err != nil {
		fmt.Println("could not parse pages:", err)
		os.Exit(1)
	}

	policies, err := parseDir(filepath.Join("static", "policies"))
	if err != nil {
		fmt.Println("could not parse policies:", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	fs := http.FileServer(http.Dir("./static"))
	mux.Handle("GET /static/", http.StripPrefix("/static/", fs))

	for id, info := range pages {
		path := fmt.Sprintf("GET /%s", id)

		if id == "index" {
			path = "GET /"
		}

		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			var tmplData TemplateData

			data, err := renderMarkdown(info.Path)
			if err != nil {
				http.Error(w, "could not render markdown", http.StatusInternalServerError)
				log.Println("render:", err)
				return
			}

			if info.Title != "" {
				tmplData.Title = info.Title
			}

			tmplData.Body = template.HTML(data)
			tmplData.Config = config

			if err := tmpl.ExecuteTemplate(w, "base.html", tmplData); err != nil {
				http.Error(w, "template error", http.StatusInternalServerError)
				log.Println("execute:", err)
			}
		})
	}

	mux.HandleFunc("GET /policy/{id}", func(w http.ResponseWriter, r *http.Request) {
		var tmplData TemplateData
		tmplData.Config = config
		id := r.PathValue("id")

		info, exists := policies[id]
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			tmplData.Title = "Policy Not Found"
			tmplData.Error = "Policy not found."
		} else {
			html, err := renderMarkdown(info.Path)
			if err != nil {
				if os.IsNotExist(err) || os.IsPermission(err) {
					w.WriteHeader(http.StatusNotFound)
				} else {
					w.WriteHeader(http.StatusInternalServerError)
				}
				tmplData.Error = "Unable to load policy document."
			} else {
				if info.Title != "" {
					tmplData.Title = info.Title
				}
				tmplData.Body = template.HTML(html)
			}
		}

		if err := tmpl.ExecuteTemplate(w, "policy.html", tmplData); err != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
			log.Println("execute:", err)
			return
		}
	})

	http.ListenAndServe(":8080", mux)
}

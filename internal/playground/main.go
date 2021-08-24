package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"goji.io"
	"goji.io/pat"

	_ "github.com/kyleconroy/sqlc"
	sqlcmd "github.com/kyleconroy/sqlc/internal/cmd"
)

const confJSON = `{
  "version": "1",
  "packages": [
    {
      "path": "db",
      "engine": "postgresql",
      "schema": "query.sql",
      "queries": "query.sql"
    }
  ]
}`

var tmpl *template.Template

type Request struct {
	Query  string `json:"query"`
	Config string `json:"config"`
}

type File struct {
	Name        string `json:"name"`
	Contents    string `json:"contents"`
	ContentType string `json:"contentType"`
}

type Response struct {
	Errored bool   `json:"errored"`
	Error   string `json:"error"`
	Sha     string `json:"sha"`
	Files   []File `json:"files"`
}

func buildOutput(dir string) (*Response, error) {
	elog := filepath.Join(dir, "out.log")
	if _, err := os.Stat(elog); err == nil {
		blob, err := ioutil.ReadFile(elog)
		if err != nil {
			return nil, err
		}
		if len(blob) > 0 {
			return &Response{Errored: true, Error: string(blob)}, nil
		}
	}
	resp := Response{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		contents, err := ioutil.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", info.Name(), err)
		}
		resp.Files = append(resp.Files, File{
			Name:        strings.TrimPrefix(path, dir+"/"),
			Contents:    string(contents),
			ContentType: "text/x-go",
		})
		return nil
	})
	return &resp, err
}

func buildInput(dir string) (*Response, error) {
	files := []string{"query.sql", "sqlc.json"}
	resp := Response{}
	for _, file := range files {
		if _, err := os.Stat(filepath.Join(dir, file)); os.IsNotExist(err) {
			continue
		}
		contents, err := ioutil.ReadFile(filepath.Join(dir, file))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		resp.Files = append(resp.Files, File{
			Name:        file,
			Contents:    string(contents),
			ContentType: fmt.Sprintf("text/x-%s", strings.ReplaceAll(filepath.Ext(file), ".", "")),
		})
	}
	return &resp, nil
}

func generate(_ context.Context, base string, rd io.Reader) (*Response, error) {
	blob, err := ioutil.ReadAll(rd)
	if err != nil {
		return nil, err
	}

	var req Request
	if err := json.Unmarshal(blob, &req); err != nil {
		return nil, err
	}

	cfg := req.Config
	if cfg == "" {
		cfg = confJSON
	}

	if req.Query == "" {
		return nil, fmt.Errorf("empty query")
	}

	h := sha256.New()
	h.Write([]byte(cfg))
	h.Write([]byte(req.Query))
	sum := fmt.Sprintf("%x", h.Sum(nil))

	dir := filepath.Join(base, sum)
	conf := filepath.Join(dir, "sqlc.json")
	query := filepath.Join(dir, "query.sql")
	elog := filepath.Join(dir, "out.log")

	// Create the directory
	_ = os.MkdirAll(dir, 0777)

	// Write the configuration file
	if err := ioutil.WriteFile(conf, []byte(cfg), 0644); err != nil {
		return nil, err
	}

	// Write the SQL
	if err := ioutil.WriteFile(query, []byte(req.Query), 0644); err != nil {
		return nil, err
	}

	// Create log
	f, err := os.Create(elog)
	if err != nil {
		return nil, err
	}

	res, err := func() (map[string]string, error) {
		var (
			_res map[string]string
			_err error
		)

		defer func() {
			if recoverErr := recover(); recoverErr != nil {
				_err = fmt.Errorf("fuck %v", recoverErr)
				return
			}
		}()
		_res, _err = sqlcmd.Generate(sqlcmd.Env{}, dir, "", f)
		if _err != nil {
			return nil, _err
		}
		return _res, nil
	}()

	for filename, source := range res {
		if err = os.MkdirAll(filepath.Dir(filename), 0777); err != nil {
			return nil, err
		}
		if err = os.WriteFile(filename, []byte(source), 0600); err != nil {
			return nil, err
		}
	}

	resp, err := buildOutput(dir)
	if err != nil {
		return nil, err
	}
	resp.Sha = sum
	return resp, nil
}

type tmplCtx struct {
	DocHost string
	Input   template.JS
	Output  template.JS
	Stderr  string
	Pkg     string
}

func handlePlay(_ context.Context, w http.ResponseWriter, gopath, pkgPath string) {
	dir := filepath.Join(gopath, pkgPath)

	var input, output template.JS
	{
		resp, err := buildInput(dir)
		if err != nil {
			log.Println("buildInput", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		payload, err := json.Marshal(resp)
		if err != nil {
			log.Println("buildInput/marshal", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		var out bytes.Buffer
		json.HTMLEscape(&out, payload)
		input = template.JS(out.String())
	}

	{
		resp, err := buildOutput(dir)
		if err != nil {
			log.Println("buildOutput", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		payload, err := json.Marshal(resp)
		if err != nil {
			log.Println("buildOutput/marshal", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		var out bytes.Buffer
		json.HTMLEscape(&out, payload)
		output = template.JS(out.String())
	}

	tctx := tmplCtx{
		Pkg:    pkgPath,
		Input:  input,
		Output: output,
	}

	w.Header().Set("Content-Type", "text/html")
	if err := tmpl.Execute(w, tctx); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

func main() {
	pwd, _ := os.Getwd()
	gopath := filepath.Join(pwd, "internal", "playground")
	if gopath == "" {
		log.Fatalf("arg: gopath is empty")
	}

	tmpl = template.Must(template.ParseFiles(filepath.Join(gopath, "index.tmpl.html")))
	port := os.Getenv("PORT")
	if port == "" {
		port = "8086"
	}

	play := goji.NewMux()
	play.HandleFunc(pat.Get("/p/:checksum"), func(w http.ResponseWriter, r *http.Request) {
		path := pat.Param(r, "checksum")
		sha, err := hex.DecodeString(path)
		if err != nil {
			http.Error(w, "Invalid SHA: hex decode failed", http.StatusBadRequest)
			return
		}
		if len(sha) != 32 {
			http.Error(w, fmt.Sprintf("Invalid SHA: length %d", len(sha)), http.StatusBadRequest)
			return
		}
		handlePlay(r.Context(), w, gopath, filepath.Join("tmp", path))
	})

	//play.HandleFunc(pat.Get("/docs/:section"), func(w http.ResponseWriter, r *http.Request) {
	//	handlePlay(r.Context(), w, gopath, filepath.Join("docs", pat.Param(r, "section")))
	//})

	play.HandleFunc(pat.Get("/"), func(w http.ResponseWriter, r *http.Request) {
		handlePlay(r.Context(), w, gopath, filepath.Join("docs", "authors"))
	})

	play.HandleFunc(pat.Post("/generate"), func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		resp, err := generate(r.Context(), filepath.Join(gopath, "tmp"), r.Body)
		if err != nil {
			fmt.Println("error", err)
			http.Error(w, `{"errored": true, "error": "500: Internal Server Error"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		_ = enc.Encode(resp)
	})

	fs := http.FileServer(http.Dir(filepath.Join(gopath, "static")))

	srv := http.NewServeMux()
	srv.Handle("/static/", http.StripPrefix("/static", fs))
	srv.Handle("/", play)

	log.Printf("starting on :%s...\n", port)
	log.Fatal(http.ListenAndServe(":"+port, srv))
}

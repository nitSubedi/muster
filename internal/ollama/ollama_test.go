package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelsMarksLoaded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"llama3:latest","size":4},{"name":"qwen2.5:7b","size":5}]}`))
		case "/api/ps":
			w.Write([]byte(`{"models":[{"name":"llama3"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := New(srv.URL).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Loaded || got[1].Loaded || got[1].Size != 5 || !strings.Contains(string(got[0].Tag), `"size":4`) {
		t.Fatalf("got %+v", got)
	}
}

func TestModelsReportsBackendDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := New(srv.URL).Models(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

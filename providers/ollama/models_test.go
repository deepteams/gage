package ollama

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deepteams/gage"
)

func TestNativeModels(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"models":[
			{"name":"llama3:latest","model":"llama3:latest"},
			{"name":"only-name"}
		]}`)
	}))
	t.Cleanup(srv.Close)

	lister, ok := New(srv.URL).(gage.ModelLister)
	if !ok {
		t.Fatal("native provider does not implement gage.ModelLister")
	}
	infos, err := lister.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/tags" {
		t.Fatalf("path = %q", gotPath)
	}
	want := []gage.ModelInfo{
		{ID: "llama3:latest", Name: "llama3:latest"},
		{ID: "only-name", Name: "only-name"},
	}
	if len(infos) != len(want) {
		t.Fatalf("got %d models, want %d", len(infos), len(want))
	}
	for i := range want {
		if infos[i] != want[i] {
			t.Fatalf("model %d = %+v, want %+v", i, infos[i], want[i])
		}
	}
}

func TestOpenAICompatModels(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"llama3:latest"}]}`)
	}))
	t.Cleanup(srv.Close)

	lister, ok := New(srv.URL, WithOpenAICompat()).(gage.ModelLister)
	if !ok {
		t.Fatal("openai-compat provider does not implement gage.ModelLister")
	}
	infos, err := lister.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("path = %q", gotPath)
	}
	if len(infos) != 1 || infos[0].ID != "llama3:latest" {
		t.Fatalf("infos = %+v", infos)
	}
}

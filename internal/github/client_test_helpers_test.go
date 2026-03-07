package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type githubAPIRoute struct {
	method  string
	path    string
	handler http.HandlerFunc
}

func githubRoute(method, path string, handler http.HandlerFunc) githubAPIRoute {
	return githubAPIRoute{
		method:  method,
		path:    path,
		handler: handler,
	}
}

func newGitHubMockClient(t *testing.T, routes ...githubAPIRoute) *APIClient {
	t.Helper()

	handlers := make(map[string]http.HandlerFunc, len(routes))
	for _, route := range routes {
		if route.method == "" || route.path == "" {
			t.Fatal("mock github route must include method and path")
		}
		if route.handler == nil {
			t.Fatalf("mock github route handler is required for %s %s", route.method, route.path)
		}
		key := route.method + " " + route.path
		if _, exists := handlers[key]; exists {
			t.Fatalf("duplicate mock github route: %s", key)
		}
		handlers[key] = route.handler
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		handler, ok := handlers[key]
		if !ok {
			t.Fatalf("unexpected request: %s", key)
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return newTestAPIClient(srv.URL)
}

func decodeJSONRequest[T any](t *testing.T, r *http.Request) T {
	t.Helper()

	var in T
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return in
}

func writeJSONResponse(t *testing.T, w http.ResponseWriter, status int, payload any) {
	t.Helper()

	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode response body: %v", err)
	}
}

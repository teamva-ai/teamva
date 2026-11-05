package httpapi

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestOpenAPICoversEveryHTTPRoute(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve contract test path")
	}
	specPath := filepath.Clean(filepath.Join(filepath.Dir(filename), "../../../protocol/openapi/teamva.yaml"))
	documented := readOpenAPIOperations(t, specPath)

	router := newRouter(newTestRepository(t), nil, "contract-test", false)
	implemented := make(map[string]bool)
	// These endpoints are dispatched by app.networkAccess before Gin. The same
	// declarations drive the production matcher and method guard; app tests
	// exercise their actual cookies, status codes and security boundaries.
	for _, method := range RemoteSessionMethods() {
		implemented[method+" "+strings.TrimPrefix(RemoteSessionPath, "/v1")] = true
	}
	for _, route := range router.Routes() {
		if route.Path == "/livez" || route.Path == "/readyz" {
			continue
		}
		path := strings.TrimPrefix(route.Path, "/v1")
		segments := strings.Split(path, "/")
		for index, segment := range segments {
			if strings.HasPrefix(segment, ":") {
				segments[index] = "{" + strings.TrimPrefix(segment, ":") + "}"
			}
		}
		implemented[route.Method+" "+strings.Join(segments, "/")] = true
	}

	var missingDocs, missingImplementation []string
	for operation := range implemented {
		if !documented[operation] {
			missingDocs = append(missingDocs, operation)
		}
	}
	for operation := range documented {
		if !implemented[operation] {
			missingImplementation = append(missingImplementation, operation)
		}
	}
	sort.Strings(missingDocs)
	sort.Strings(missingImplementation)
	if len(missingDocs) > 0 || len(missingImplementation) > 0 {
		t.Fatalf("OpenAPI drift: undocumented=%v unimplemented=%v", missingDocs, missingImplementation)
	}
}

func readOpenAPIOperations(t *testing.T, path string) map[string]bool {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open OpenAPI: %v", err)
	}
	defer file.Close()

	operations := make(map[string]bool)
	var currentPath string
	methods := map[string]string{
		"get:": "GET", "post:": "POST", "put:": "PUT",
		"patch:": "PATCH", "delete:": "DELETE",
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":") {
			currentPath = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		if currentPath == "" || !strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "      ") {
			continue
		}
		if method, ok := methods[strings.TrimSpace(line)]; ok {
			key := method + " " + currentPath
			if operations[key] {
				t.Fatalf("duplicate OpenAPI operation %s", key)
			}
			operations[key] = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan OpenAPI: %v", err)
	}
	return operations
}

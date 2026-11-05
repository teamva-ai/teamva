//go:build darwin || linux

package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/node/provider"
)

func TestReadOnlyInstanceCannotBeRelaxedByStartOrResume(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, resume := range []bool{false, true} {
		for _, withIPC := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "start", true: "resume"}[resume], map[bool]string{false: "no-ipc", true: "ipc"}[withIPC]}, "/"), func(t *testing.T) {
				dir := t.TempDir()
				logPath := filepath.Join(dir, "requests.jsonl")
				executable := filepath.Join(dir, "fake-codex")
				script := strings.ReplaceAll(`#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "$line" >> '__LOG__'
  id=${line#*\"id\":}
  id=${id%%,*}
  case "$line" in
    *'"method":"initialize"'*) printf '{"id":%s,"result":{"userAgent":"teamva/0.163.0"}}\n' "$id" ;;
    *'"method":"thread/start"'*|*'"method":"thread/resume"'*)
      roots=${line#*\"runtimeWorkspaceRoots\":}
      roots=${roots%%]*}
      printf '{"id":%s,"result":{"thread":{"id":"fixture-thread"},"activePermissionProfile":{"id":"teamva"},"runtimeWorkspaceRoots":%s]}}\n' "$id" "$roots" ;;
    *'"method":"turn/start"'*)
      printf '{"id":%s,"result":{"turn":{"id":"fixture-turn"}}}\n' "$id"
      printf '{"method":"turn/completed","params":{"turn":{"id":"fixture-turn","status":"completed"}}}\n' ;;
  esac
done
`, "__LOG__", logPath)
				if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				item, err := newInstance(provider.InstanceOptions{Executable: executable, ConfigDir: t.TempDir(), DataDir: t.TempDir(), Config: provider.InstanceConfig{
					ReadOnly: true, WritableRepositories: []string{dir},
				}})
				if err != nil {
					t.Fatal(err)
				}
				defer item.Close()
				request := provider.RunRequest{RunID: "readonly-instance", CWD: dir, Prompt: "fixture"}
				wantRoots := []string{}
				if withIPC {
					request.ToolRequestDir = t.TempDir()
					wantRoots = append(wantRoots, request.ToolRequestDir)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				run := item.Start
				if resume {
					request.NativeSessionID = "fixture-thread"
					run = item.Resume
				}
				if _, err := run(ctx, request, func(context.Context, provider.Event) error { return nil }); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(logPath)
				if err != nil {
					t.Fatal(err)
				}
				checked := 0
				for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
					var message struct {
						Method string `json:"method"`
						Params struct {
							Roots    []string `json:"runtimeWorkspaceRoots"`
							Approval any      `json:"approvalPolicy"`
						} `json:"params"`
					}
					if err := json.Unmarshal([]byte(line), &message); err != nil {
						t.Fatal(err)
					}
					if message.Method == "thread/start" || message.Method == "thread/resume" || message.Method == "turn/start" {
						checked++
						if !slices.Equal(message.Params.Roots, wantRoots) || message.Params.Approval != "never" {
							t.Fatalf("%s relaxed read-only instance: roots=%v approval=%v", message.Method, message.Params.Roots, message.Params.Approval)
						}
					}
				}
				if checked != 2 {
					t.Fatalf("checked %d permission envelopes, want thread and turn", checked)
				}
			})
		}
	}
}

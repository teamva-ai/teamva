package app

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func TestLocalApplicationReopensDataDirectoryAfterSIGKILL(t *testing.T) {
	if os.Getenv("TEAMVA_LOCAL_CRASH_HELPER") == "1" {
		runLocalApplicationCrashHelper(t)
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL recovery test requires a Unix process model")
	}
	dataDir := filepath.Join(t.TempDir(), "local-data")
	command := exec.Command(os.Args[0], "-test.run=^TestLocalApplicationReopensDataDirectoryAfterSIGKILL$")
	command.Env = append(os.Environ(),
		"TEAMVA_LOCAL_CRASH_HELPER=1",
		"TEAMVA_LOCAL_CRASH_DATA_DIR="+dataDir,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == "LOCAL-CRASH-READY" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatal("local crash helper exited before becoming ready")
		}
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal("local crash helper did not become ready")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("SIGKILL helper exited successfully")
	}

	service, err := StartLocal(t.Context(), LocalConfig{
		Address: "127.0.0.1:0", DataDir: dataDir, BootstrapNonce: "restart-bootstrap",
		DisableRuntime: true,
	})
	if err != nil {
		t.Fatalf("restart local application after SIGKILL: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Errorf("close restarted local application: %v", err)
		}
	})
	user, computer, err := service.repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	projects, err := service.repository.ListProjects(t.Context(), user.ID, computer.ID)
	if err != nil || len(projects) != 2 || !projects[0].IsDefault || projects[0].ID != domain.DefaultProjectID(computer.ID) || projects[1].Name != "SIGKILL persisted project" || projects[1].IsDefault {
		t.Fatalf("recovered projects=%+v err=%v", projects, err)
	}
	issues, err := service.repository.ListIssues(t.Context(), user.ID, projects[1].ID)
	if err != nil || len(issues) != 1 || issues[0].Title != "SIGKILL persisted issue" {
		t.Fatalf("recovered issues=%+v err=%v", issues, err)
	}
	comments, err := service.repository.ListIssueComments(t.Context(), user.ID, issues[0].ID)
	if err != nil || len(comments) != 1 || comments[0].Body != "persisted before abrupt termination" {
		t.Fatalf("recovered comments=%+v err=%v", comments, err)
	}
}

func runLocalApplicationCrashHelper(t *testing.T) {
	service, err := StartLocal(context.Background(), LocalConfig{
		Address: "127.0.0.1:0", DataDir: os.Getenv("TEAMVA_LOCAL_CRASH_DATA_DIR"),
		BootstrapNonce: "crash-bootstrap", DisableRuntime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	user, computer, err := service.repository.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	project, err := service.repository.CreateProject(context.Background(), user.ID, domain.Project{
		ComputerID: computer.ID, Name: "SIGKILL persisted project",
	})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := service.repository.CreateIssue(context.Background(), user.ID, domain.Issue{
		ProjectID: project.ID, Title: "SIGKILL persisted issue", Status: domain.IssueOpen,
		CreatedByUserID: user.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.repository.CreateIssueComment(context.Background(), user.ID, domain.IssueComment{
		IssueID: issue.ID, AuthorKind: domain.IssueCommentAuthorUser,
		AuthorUserID: user.ID, Body: "persisted before abrupt termination",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(os.Stdout, "LOCAL-CRASH-READY"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Minute)
}

package store

import (
	"context"
	"testing"
)

func TestSQLiteSkillReinstallAfterUninstallReusesImmutableVersion(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	t.Cleanup(func() { _ = f.repository.Close() })
	installed := installSkillUninstallTestPackage(t, f)
	artifact, err := f.repository.GetSkillPackageVersion(t.Context(), f.user.ID, installed.SkillPackageVersionID)
	if err != nil {
		t.Fatal(err)
	}
	originalVersionID := installed.SkillPackageVersionID
	for cycle := 0; cycle < 3; cycle++ {
		if err := f.repository.UninstallComputerSkillPackageWithArtifact(t.Context(), f.user.ID,
			f.computer.ID, installed.ID, installed.Version, func(context.Context, string) error { return nil }); err != nil {
			t.Fatal(err)
		}
		previousID := installed.ID
		previousVersion := installed.Version
		installed, err = f.repository.InstallComputerSkillPackageVersion(t.Context(), f.user.ID, f.computer.ID, installed.SkillPackageID, artifact)
		if err != nil {
			t.Fatalf("reinstall cycle %d failed: %v", cycle, err)
		}
		if installed.ID != previousID || installed.SkillPackageVersionID != originalVersionID || installed.Version != previousVersion+2 {
			t.Fatalf("reinstall must reactivate the installation with a newer CAS version: %+v", installed)
		}
		assertDomainCode(t, f.repository.UninstallComputerSkillPackageWithArtifact(t.Context(), f.user.ID,
			f.computer.ID, installed.ID, previousVersion, func(context.Context, string) error {
				t.Fatal("stale pre-uninstall version reached artifact deletion")
				return nil
			}), "version_conflict")
		items, err := f.repository.ListComputerSkillPackages(t.Context(), f.user.ID, f.computer.ID)
		if err != nil || len(items) != 1 || items[0].ID != installed.ID {
			t.Fatalf("reinstalled skill is not visible: items=%+v err=%v", items, err)
		}
	}
	var versionCount, installationCount int
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM skill_package_versions WHERE skill_package_id = ?`, installed.SkillPackageID).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.db.QueryRowContext(t.Context(), `SELECT count(*) FROM computer_skill_packages WHERE skill_package_version_id = ?`, originalVersionID).Scan(&installationCount); err != nil {
		t.Fatal(err)
	}
	if versionCount != 1 || installationCount != 1 {
		t.Fatalf("reinstall duplicated immutable version or installation: versions=%d installations=%d", versionCount, installationCount)
	}
}

func TestSQLiteSkillReinstallRejectsChangedContentAtSameCommit(t *testing.T) {
	f := newSQLiteAgentTestFixture(t)
	t.Cleanup(func() { _ = f.repository.Close() })
	installed := installSkillUninstallTestPackage(t, f)
	artifact, err := f.repository.GetSkillPackageVersion(t.Context(), f.user.ID, installed.SkillPackageVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repository.UninstallComputerSkillPackage(t.Context(), f.user.ID, f.computer.ID, installed.ID, installed.Version); err != nil {
		t.Fatal(err)
	}
	artifact.ContentDigest = "sha256:different-content"
	_, err = f.repository.InstallComputerSkillPackageVersion(t.Context(), f.user.ID, f.computer.ID, installed.SkillPackageID, artifact)
	assertDomainCode(t, err, "skill_version_conflict")
	current, err := f.repository.GetSkillPackageVersion(t.Context(), f.user.ID, installed.SkillPackageVersionID)
	if err != nil || current.ContentDigest != "sha256:test" || current.Version != 1 {
		t.Fatalf("reinstall overwrote immutable content: %+v err=%v", current, err)
	}
	items, err := f.repository.ListComputerSkillPackages(t.Context(), f.user.ID, f.computer.ID)
	if err != nil || len(items) != 0 {
		t.Fatalf("conflicting reinstall published installation: items=%+v err=%v", items, err)
	}
	artifact.ContentDigest = current.ContentDigest
	if _, err := f.repository.InstallComputerSkillPackageVersion(t.Context(), f.user.ID, f.computer.ID, installed.SkillPackageID, artifact); err != nil {
		t.Fatalf("verified original content could not be reinstalled: %v", err)
	}
}

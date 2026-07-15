package main

import (
	"os"
	"path/filepath"
	"testing"

	knowledgeauthoring "handshake/skills/knowledge-authoring"
)

func TestInstallKnowledgeAuthoringSkills_InstallsAndUpdatesManagedFiles(t *testing.T) {
	homeDir := t.TempDir()
	results := installKnowledgeAuthoringSkills(homeDir)
	if len(results) != 4 {
		t.Fatalf("installed targets = %d, want 4", len(results))
	}
	for _, result := range results {
		if result.err != nil || result.status != knowledgeSkillInstalled {
			t.Fatalf("first install %s = %+v", result.target.agent, result)
		}
		contents, err := os.ReadFile(result.target.path)
		if err != nil {
			t.Fatalf("read %s: %v", result.target.agent, err)
		}
		if string(contents) != string(knowledgeauthoring.Definition) {
			t.Fatalf("installed %s skill differs from embedded definition", result.target.agent)
		}
	}

	target := knowledgeSkillTargets(homeDir)[0]
	if err := os.WriteFile(target.path, []byte("---\nmetadata:\n  handshake-managed: \"true\"\n---\nold"), 0600); err != nil {
		t.Fatalf("write old managed skill: %v", err)
	}
	results = installKnowledgeAuthoringSkills(homeDir)
	if results[0].status != knowledgeSkillUpdated || results[0].err != nil {
		t.Fatalf("managed update = %+v", results[0])
	}
	contents, err := os.ReadFile(target.path)
	if err != nil || string(contents) != string(knowledgeauthoring.Definition) {
		t.Fatalf("managed skill was not updated: %v", err)
	}
}

func TestInstallKnowledgeAuthoringSkills_PreservesUserOwnedSkill(t *testing.T) {
	homeDir := t.TempDir()
	target := knowledgeSkillTargets(homeDir)[1]
	if err := os.MkdirAll(filepath.Dir(target.path), 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const userSkill = "---\nname: knowledge-authoring\n---\nuser instructions"
	if err := os.WriteFile(target.path, []byte(userSkill), 0600); err != nil {
		t.Fatalf("write user skill: %v", err)
	}

	results := installKnowledgeAuthoringSkills(homeDir)
	if results[1].status != knowledgeSkillUserOwned || results[1].err != nil {
		t.Fatalf("user-owned result = %+v", results[1])
	}
	contents, err := os.ReadFile(target.path)
	if err != nil || string(contents) != userSkill {
		t.Fatalf("user-owned skill was overwritten: %q, %v", contents, err)
	}
}

func TestRemoveKnowledgeAuthoringSkills_RemovesOnlyManagedFiles(t *testing.T) {
	homeDir := t.TempDir()
	installKnowledgeAuthoringSkills(homeDir)
	userTarget := knowledgeSkillTargets(homeDir)[2]
	if err := os.WriteFile(userTarget.path, []byte("---\nname: knowledge-authoring\n---\nuser instructions"), 0600); err != nil {
		t.Fatalf("replace with user skill: %v", err)
	}

	removeKnowledgeAuthoringSkills(homeDir)
	for index, target := range knowledgeSkillTargets(homeDir) {
		_, err := os.Stat(target.path)
		if index == 2 {
			if err != nil {
				t.Fatalf("user-owned skill was removed: %v", err)
			}
			continue
		}
		if !os.IsNotExist(err) {
			t.Fatalf("managed %s skill still exists or could not be checked: %v", target.agent, err)
		}
	}
}

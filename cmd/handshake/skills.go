package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	handshakeskill "handshake/skills/handshake"
	knowledgeauthoring "handshake/skills/knowledge-authoring"
)

const knowledgeSkillName = "knowledge-authoring"
const handshakeSkillName = "handshake"

type knowledgeSkillTarget struct {
	agent string
	path  string
}

type knowledgeSkillInstallStatus string

const (
	knowledgeSkillInstalled knowledgeSkillInstallStatus = "installed"
	knowledgeSkillUpdated   knowledgeSkillInstallStatus = "updated"
	knowledgeSkillUserOwned knowledgeSkillInstallStatus = "user-owned"
)

type knowledgeSkillInstallResult struct {
	skill  string
	target knowledgeSkillTarget
	status knowledgeSkillInstallStatus
	err    error
}

func knowledgeSkillTargets(homeDir string) []knowledgeSkillTarget {
	return []knowledgeSkillTarget{
		{agent: "Claude Code", path: filepath.Join(homeDir, ".claude", "skills", knowledgeSkillName, "SKILL.md")},
		{agent: "Codex", path: filepath.Join(homeDir, ".codex", "skills", knowledgeSkillName, "SKILL.md")},
		{agent: "OpenCode", path: filepath.Join(homeDir, ".config", "opencode", "skills", knowledgeSkillName, "SKILL.md")},
		{agent: "Hermes", path: filepath.Join(homeDir, ".hermes", "skills", knowledgeSkillName, "SKILL.md")},
	}
}

func handshakeSkillTargets(homeDir string) []knowledgeSkillTarget {
	return []knowledgeSkillTarget{
		{agent: "Claude Code", path: filepath.Join(homeDir, ".claude", "skills", handshakeSkillName, "SKILL.md")},
		{agent: "Codex", path: filepath.Join(homeDir, ".codex", "skills", handshakeSkillName, "SKILL.md")},
		{agent: "OpenCode", path: filepath.Join(homeDir, ".config", "opencode", "skills", handshakeSkillName, "SKILL.md")},
		{agent: "Hermes", path: filepath.Join(homeDir, ".hermes", "skills", handshakeSkillName, "SKILL.md")},
	}
}

func installKnowledgeAuthoringSkills(homeDir string) []knowledgeSkillInstallResult {
	return installManagedSkills(knowledgeSkillName, knowledgeSkillTargets(homeDir), knowledgeauthoring.Definition)
}

func installHandshakeSkills(homeDir string) []knowledgeSkillInstallResult {
	return installManagedSkills(handshakeSkillName, handshakeSkillTargets(homeDir), handshakeskill.Definition)
}

func installManagedSkills(skill string, targets []knowledgeSkillTarget, definition []byte) []knowledgeSkillInstallResult {
	results := make([]knowledgeSkillInstallResult, 0, len(targets))
	for _, target := range targets {
		result := knowledgeSkillInstallResult{skill: skill, target: target}
		existing, err := os.ReadFile(target.path)
		exists := err == nil
		switch {
		case err == nil && !isHandshakeManagedSkill(existing):
			result.status = knowledgeSkillUserOwned
		case err != nil && !os.IsNotExist(err):
			result.err = err
		case err == nil && string(existing) == string(definition):
			result.status = knowledgeSkillUpdated
		default:
			if err := writeFileAtomic(target.path, definition, 0644); err != nil {
				result.err = err
			} else if exists {
				result.status = knowledgeSkillUpdated
			} else {
				result.status = knowledgeSkillInstalled
			}
		}
		results = append(results, result)
	}
	return results
}

func printKnowledgeSkillInstallResults(results []knowledgeSkillInstallResult) {
	for _, result := range results {
		switch {
		case result.err != nil:
			fmt.Printf("✗ %s: could not install %s skill: %v\n", result.target.agent, result.skill, result.err)
		case result.status == knowledgeSkillUserOwned:
			fmt.Printf("- %s: kept user-owned %s skill at %s\n", result.target.agent, result.skill, result.target.path)
		case result.status == knowledgeSkillInstalled:
			fmt.Printf("✓ %s: %s skill installed\n", result.target.agent, result.skill)
		default:
			fmt.Printf("✓ %s: %s skill updated\n", result.target.agent, result.skill)
		}
	}
}

func removeKnowledgeAuthoringSkills(homeDir string) {
	removeManagedSkills(knowledgeSkillTargets(homeDir))
}

func removeHandshakeSkills(homeDir string) {
	removeManagedSkills(handshakeSkillTargets(homeDir))
}

func removeManagedSkills(targets []knowledgeSkillTarget) {
	for _, target := range targets {
		contents, err := os.ReadFile(target.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			fmt.Printf("✗ %s skill: could not inspect: %v\n", target.agent, err)
			continue
		}
		if !isHandshakeManagedSkill(contents) {
			fmt.Printf("- %s skill: kept user-owned %s\n", target.agent, target.path)
			continue
		}
		if err := os.Remove(target.path); err != nil {
			fmt.Printf("✗ %s skill: could not remove: %v\n", target.agent, err)
			continue
		}
		// Remove only the now-empty Handshake-owned skill directory. Never walk
		// upward into agent or user configuration directories.
		_ = os.Remove(filepath.Dir(target.path))
		fmt.Printf("✓ %s skill: removed\n", target.agent)
	}
}

func isHandshakeManagedSkill(contents []byte) bool {
	return strings.Contains(string(contents), "handshake-managed: \"true\"")
}

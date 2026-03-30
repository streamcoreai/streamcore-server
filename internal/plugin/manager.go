package plugin

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Manager discovers, loads, and manages all plugins and skills.
type Manager struct {
	mu      sync.RWMutex
	plugins map[string]Tool
	skills  []Skill
	dir     string
	sdkDir  string // set via PLUGIN_SDK_DIR env var (dev only)
}

// NewManager creates a plugin manager that scans the given directory.
// If the PLUGIN_SDK_DIR environment variable is set, the manager injects
// SDK paths into plugin subprocesses (development use only — production
// plugins install the SDK via pip/npm).
func NewManager(pluginDir string) *Manager {
	sdkDir := os.Getenv("PLUGIN_SDK_DIR")
	if sdkDir != "" {
		if abs, err := filepath.Abs(sdkDir); err == nil {
			sdkDir = abs
		}
	}
	return &Manager{
		plugins: make(map[string]Tool),
		dir:     pluginDir,
		sdkDir:  sdkDir,
	}
}

// LoadAll discovers and starts all plugins and skills in the configured directory.
func (m *Manager) LoadAll(ctx context.Context) error {
	if m.dir == "" {
		log.Println("[plugins] no plugin directory configured, skipping")
		return nil
	}

	absDir, err := filepath.Abs(m.dir)
	if err != nil {
		return fmt.Errorf("resolve plugin directory: %w", err)
	}

	// Create directory if it doesn't exist
	if err := os.MkdirAll(absDir, 0755); err != nil {
		return fmt.Errorf("create plugin directory %s: %w", absDir, err)
	}

	pluginsDir := filepath.Join(absDir, "plugins")
	skillsDir := filepath.Join(absDir, "skills")

	os.MkdirAll(pluginsDir, 0755)
	os.MkdirAll(skillsDir, 0755)

	if err := m.loadPlugins(ctx, pluginsDir); err != nil {
		return fmt.Errorf("load plugins: %w", err)
	}

	if err := m.loadSkills(skillsDir); err != nil {
		return fmt.Errorf("load skills: %w", err)
	}

	log.Printf("[plugins] loaded %d plugins, %d skills from %s", len(m.plugins), len(m.skills), absDir)
	return nil
}

// RegisterNative adds a Go-native tool to the manager.
func (m *Manager) RegisterNative(tool Tool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plugins[tool.Name()] = tool
	log.Printf("[plugins] registered native plugin: %s", tool.Name())
}

// Tools returns all registered tools (both external and native).
func (m *Manager) Tools() []Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tools := make([]Tool, 0, len(m.plugins))
	for _, t := range m.plugins {
		tools = append(tools, t)
	}
	return tools
}

// GetTool returns a tool by name.
func (m *Manager) GetTool(name string) (Tool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.plugins[name]
	return t, ok
}

// Skills returns all loaded skills.
func (m *Manager) Skills() []Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.skills
}

// SkillsPrompt returns the combined skill instructions as a system prompt
// supplement for the LLM.
func (m *Manager) SkillsPrompt() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.skills) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\n--- Skills ---\n")
	for _, s := range m.skills {
		b.WriteString(fmt.Sprintf("\n## Skill: %s\n", s.Name))
		if s.Description != "" {
			b.WriteString(fmt.Sprintf("Description: %s\n", s.Description))
		}
		if len(s.Triggers) > 0 {
			b.WriteString(fmt.Sprintf("Triggers: %s\n", strings.Join(s.Triggers, ", ")))
		}
		b.WriteString(s.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// Close stops all external plugin processes.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, t := range m.plugins {
		if ep, ok := t.(*ExternalPlugin); ok {
			ep.Stop()
			log.Printf("[plugins] stopped plugin: %s", name)
		}
	}
}

// loadPlugins scans the plugins directory for plugin.yaml manifests.
func (m *Manager) loadPlugins(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pluginDir := filepath.Join(dir, entry.Name())
		manifestPath := filepath.Join(pluginDir, "plugin.yaml")

		data, err := os.ReadFile(manifestPath)
		if err != nil {
			if os.IsNotExist(err) {
				log.Printf("[plugins] skipping %s: no plugin.yaml", entry.Name())
				continue
			}
			return fmt.Errorf("read %s: %w", manifestPath, err)
		}

		var manifest Manifest
		if err := yaml.Unmarshal(data, &manifest); err != nil {
			log.Printf("[plugins] invalid manifest in %s: %v", entry.Name(), err)
			continue
		}

		if manifest.Name == "" {
			log.Printf("[plugins] skipping %s: manifest missing name", entry.Name())
			continue
		}

		if manifest.Entrypoint == "" {
			log.Printf("[plugins] skipping %s: manifest missing entrypoint", entry.Name())
			continue
		}

		plugin := NewExternalPlugin(manifest, pluginDir, m.sdkDir)
		if err := plugin.Start(ctx); err != nil {
			log.Printf("[plugins] failed to start %s: %v", manifest.Name, err)
			continue
		}

		m.mu.Lock()
		m.plugins[manifest.Name] = plugin
		m.mu.Unlock()

		log.Printf("[plugins] loaded: %s (lang=%s, v%d)", manifest.Name, manifest.Language, manifest.Version)
	}

	return nil
}

// loadSkills scans the skills directory for SKILL.md files with YAML frontmatter.
func (m *Manager) loadSkills(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read %s: %w", skillPath, err)
		}

		skill, err := parseSkill(string(data))
		if err != nil {
			log.Printf("[plugins] invalid skill in %s: %v", entry.Name(), err)
			continue
		}

		m.mu.Lock()
		m.skills = append(m.skills, skill)
		m.mu.Unlock()

		log.Printf("[plugins] loaded skill: %s", skill.Name)
	}

	return nil
}

// parseSkill parses a SKILL.md file with YAML frontmatter (--- delimited).
func parseSkill(content string) (Skill, error) {
	var skill Skill

	// Split frontmatter from content
	scanner := bufio.NewScanner(strings.NewReader(content))
	inFrontmatter := false
	var frontmatter strings.Builder
	var body strings.Builder
	frontmatterDone := false

	for scanner.Scan() {
		line := scanner.Text()
		if !frontmatterDone && strings.TrimSpace(line) == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			}
			// End of frontmatter
			inFrontmatter = false
			frontmatterDone = true
			continue
		}

		if inFrontmatter {
			frontmatter.WriteString(line + "\n")
		} else if frontmatterDone {
			body.WriteString(line + "\n")
		}
	}

	if frontmatter.Len() == 0 {
		return skill, fmt.Errorf("no YAML frontmatter found")
	}

	if err := yaml.Unmarshal([]byte(frontmatter.String()), &skill); err != nil {
		return skill, fmt.Errorf("parse frontmatter: %w", err)
	}

	skill.Content = strings.TrimSpace(body.String())

	if skill.Name == "" {
		return skill, fmt.Errorf("skill missing name")
	}

	return skill, nil
}

package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Manager discovers, loads, and manages all plugins and skills.
type Manager struct {
	mu            sync.RWMutex
	plugins       map[string]Tool
	eventHandlers map[string]EventHandler
	hosts         []*ExternalPlugin
	skills        []Skill
	dir           string
	sdkDir        string // set via PLUGIN_SDK_DIR env var (dev only)
	confirmations *ConfirmationStore

	// settings are the per-plugin tables from config.toml, keyed by plugin
	// name and handed over at initialize.
	settings map[string]json.RawMessage

	// sinks route a plugin's callbacks to the conversation they concern. The
	// manager outlives any one session, so a plugin reaches a session through
	// here rather than holding one.
	sinkMu sync.RWMutex
	sinks  map[string]SessionSink

	closeOnce sync.Once
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
		plugins:       make(map[string]Tool),
		eventHandlers: make(map[string]EventHandler),
		dir:           pluginDir,
		sdkDir:        sdkDir,
		confirmations: NewConfirmationStore(0),
		settings:      make(map[string]json.RawMessage),
		sinks:         make(map[string]SessionSink),
	}
}

// Confirmations returns the store backing the two-call gate for tools that
// declare ConfirmationRequired. It is shared by every session; tokens are
// bound to the session that was issued them.
func (m *Manager) Confirmations() *ConfirmationStore {
	return m.confirmations
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

	m.announceReady(ctx)

	log.Printf("[plugins] loaded %d plugins, %d skills from %s", len(m.plugins), len(m.skills), absDir)
	return nil
}

// RegisterTool adds an in-process tool.
//
// This is not how plugins are written — every plugin is a subprocess with a
// manifest, in whatever language its author likes. It exists so the host and
// the pipeline can be tested against a tool that answers immediately, without
// starting a process to do it.
func (m *Manager) RegisterTool(tool Tool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plugins[tool.Name()] = tool
	if handler, ok := tool.(EventHandler); ok {
		m.eventHandlers[tool.Name()] = handler
	}
}

// RegisterEventHandler adds an in-process event handler. It is not returned by
// Tools and therefore never becomes a model-callable function. Same purpose as
// RegisterTool: a test seam, not a plugin authoring path.
func (m *Manager) RegisterEventHandler(name string, handler EventHandler) {
	if handler == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventHandlers[name] = handler
}

// Tools returns the tools the model may call. A tool marked internal is left
// out: it exists for other plugins to reach, and offering it to the model would
// only invite calls nobody wants.
func (m *Manager) Tools() []Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tools := make([]Tool, 0, len(m.plugins))
	for _, t := range m.plugins {
		if isInternal(t) {
			continue
		}
		tools = append(tools, t)
	}
	return tools
}

// GetTool returns a model-callable tool by name. Internal tools are not
// reachable here, so a model that guesses one of their names gets the same
// "unknown tool" it would get for anything else it invented.
func (m *Manager) GetTool(name string) (Tool, bool) {
	tool, ok := m.lookup(name)
	if !ok || isInternal(tool) {
		return nil, false
	}
	return tool, true
}

// lookup finds any registered tool, internal ones included. It backs the
// plugin-to-plugin path, which is the only caller allowed to reach them.
func (m *Manager) lookup(name string) (Tool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.plugins[name]
	return t, ok
}

func isInternal(tool Tool) bool {
	marked, ok := tool.(InternalTool)
	return ok && marked.Internal()
}

// DispatchEvent delivers a lifecycle event to every handler that subscribed to
// its type. All matching handlers run even when one fails; errors are joined so
// a broken observer cannot suppress the output of a healthy one.
func (m *Manager) DispatchEvent(ctx context.Context, event PluginEvent) ([]OutboundEvent, error) {
	m.mu.RLock()
	handlers := make([]EventHandler, 0, len(m.eventHandlers))
	for _, handler := range m.eventHandlers {
		for _, eventType := range handler.Events() {
			if eventType == event.Type {
				handlers = append(handlers, handler)
				break
			}
		}
	}
	m.mu.RUnlock()

	var outputs []OutboundEvent
	var errs []error
	for _, handler := range handlers {
		out, err := handler.HandleEvent(ctx, event)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		outputs = append(outputs, out...)
	}
	return outputs, errors.Join(errs...)
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

// Close stops every plugin process.
//
// Plugins are stopped concurrently: each gets a grace period to exit cleanly,
// and running those in series would multiply the wait by the number of plugins
// while the server's shutdown deadline stays fixed.
//
// It is safe to call more than once, so a deferred Close cannot double-stop
// plugins an explicit shutdown already dealt with.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		hosts := append([]*ExternalPlugin(nil), m.hosts...)
		m.mu.Unlock()

		var wg sync.WaitGroup
		for _, host := range hosts {
			wg.Add(1)
			go func(host *ExternalPlugin) {
				defer wg.Done()
				host.Stop()
				log.Printf("[plugins] stopped plugin: %s", host.manifest.Name)
			}(host)
		}
		wg.Wait()
	})
}

// loadPlugins scans the plugins directory for plugin.yaml manifests.
//
// A plugin that fails to load is skipped with a log line rather than failing
// startup. One broken plugin folder must not take a voice deployment down.
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
		manifest.Normalize()
		if err := m.load(ctx, manifest, pluginDir); err != nil {
			log.Printf("[plugins] skipping %s: %v", entry.Name(), err)
		}
	}

	return nil
}

// load brings one manifest up: dispatch tools need nothing started, and
// anything else gets a process whose tools are registered once it answers.
func (m *Manager) load(ctx context.Context, manifest Manifest, dir string) error {
	if err := manifest.Validate(); err != nil {
		return err
	}

	m.mu.RLock()
	manifest.config = m.settings[manifest.Name]
	m.mu.RUnlock()

	if !manifest.IsEnabled() {
		log.Printf("[plugins] %s is disabled", manifest.Name)
		return nil
	}

	if !manifest.NeedsProcess() {
		for _, spec := range manifest.Tools {
			tool, err := NewDispatchTool(spec)
			if err != nil {
				return err
			}
			m.register(tool)
		}
		log.Printf("[plugins] loaded: %s (%d dispatch tools, v%d)",
			manifest.Name, len(manifest.Tools), manifest.Version)
		return nil
	}

	host := NewExternalPlugin(manifest, dir, m.sdkDir)
	host.SetCallbacks(m.callbacks())
	if err := host.Start(ctx); err != nil {
		return err
	}

	if err := m.registerHostTools(host, host.ToolSpecs()); err != nil {
		return err
	}

	m.mu.Lock()
	m.hosts = append(m.hosts, host)
	if len(manifest.Events) > 0 {
		m.eventHandlers[manifest.Name] = host
	}
	m.mu.Unlock()

	log.Printf("[plugins] loaded: %s (%d tools, v%d)", manifest.Name, len(host.ToolSpecs()), manifest.Version)
	return nil
}

// registerHostTools replaces everything a host had registered with the specs it
// now advertises, so a revised list at ready does not leave the old one behind.
func (m *Manager) registerHostTools(host *ExternalPlugin, specs []ToolSpec) error {
	tools := make([]Tool, 0, len(specs))
	for _, spec := range specs {
		if spec.Dispatch != nil {
			// A dispatch block short-circuits the process even inside a plugin
			// that has one: no reason to pay a round trip for a packet the
			// manifest already describes in full.
			tool, err := NewDispatchTool(spec)
			if err != nil {
				return err
			}
			tools = append(tools, tool)
			continue
		}
		tools = append(tools, &externalTool{host: host, spec: spec})
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for name, existing := range m.plugins {
		if owned, ok := existing.(*externalTool); ok && owned.host == host {
			delete(m.plugins, name)
		}
	}
	for _, tool := range tools {
		m.plugins[tool.Name()] = tool
	}
	return nil
}

// announceReady runs the second declaration pass once every plugin is up.
//
// Until now a plugin could only describe itself in isolation. Here it learns
// what else loaded and may revise what it offers, which is how one plugin
// exposes a tool that only makes sense when another is present — without
// either of them depending on load order.
func (m *Manager) announceReady(ctx context.Context) {
	m.mu.RLock()
	hosts := append([]*ExternalPlugin(nil), m.hosts...)
	available := make([]string, 0, len(m.plugins))
	for name := range m.plugins {
		available = append(available, name)
	}
	m.mu.RUnlock()
	sort.Strings(available)

	for _, host := range hosts {
		revised, err := host.Ready(ctx, available)
		if err != nil {
			log.Printf("[plugins] %s failed its ready pass: %v", host.manifest.Name, err)
			continue
		}
		if revised == nil {
			continue
		}
		if err := m.registerHostTools(host, revised); err != nil {
			log.Printf("[plugins] %s revised its tools badly: %v", host.manifest.Name, err)
			continue
		}
		log.Printf("[plugins] %s revised its tools: %d", host.manifest.Name, len(revised))
	}
}

func (m *Manager) register(tool Tool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plugins[tool.Name()] = tool
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

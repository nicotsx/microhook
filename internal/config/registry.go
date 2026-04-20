package config

import (
	"fmt"
	"maps"
	"strings"
)

type ActionRegistry struct {
	ordered       []Action
	byName        map[string]Action
	enabledByName map[string]Action
}

func (r ActionRegistry) Len() int {
	return len(r.ordered)
}

func (r ActionRegistry) All() []Action {
	actions := make([]Action, 0, len(r.ordered))
	for _, action := range r.ordered {
		actions = append(actions, cloneAction(action))
	}

	return actions
}

func (r ActionRegistry) Get(name string) (Action, bool) {
	action, ok := r.byName[name]
	if !ok {
		return Action{}, false
	}

	return cloneAction(action), true
}

func (r ActionRegistry) Enabled(name string) (Action, bool) {
	action, ok := r.enabledByName[name]
	if !ok {
		return Action{}, false
	}

	return cloneAction(action), true
}

func (c Config) ActionRegistry() ActionRegistry {
	if c.actionRegistry.byName != nil {
		return c.actionRegistry
	}

	registry, err := newActionRegistry(c.Actions)
	if err != nil {
		return ActionRegistry{}
	}

	return registry
}

func (c Config) Action(name string) (Action, bool) {
	return c.ActionRegistry().Get(name)
}

func (c Config) EnabledAction(name string) (Action, bool) {
	return c.ActionRegistry().Enabled(name)
}

func newActionRegistry(actions []Action) (ActionRegistry, error) {
	registry := ActionRegistry{
		ordered:       make([]Action, 0, len(actions)),
		byName:        make(map[string]Action, len(actions)),
		enabledByName: make(map[string]Action, len(actions)),
	}

	seenNames := make(map[string]int, len(actions))
	var errs ValidationErrors

	for i, action := range actions {
		name := strings.TrimSpace(action.Name)
		if name == "" {
			errs.Add(fmt.Sprintf("actions[%d].name is required", i))
			continue
		}

		if previous, exists := seenNames[name]; exists {
			errs.Add(fmt.Sprintf("actions[%d].name duplicates actions[%d].name (%q)", i, previous, name))
			continue
		}

		seenNames[name] = i
		cloned := cloneAction(action)
		registry.ordered = append(registry.ordered, cloned)
		registry.byName[cloned.Name] = cloned
		if cloned.Enabled {
			registry.enabledByName[cloned.Name] = cloned
		}
	}

	if err := errs.OrNil(); err != nil {
		return ActionRegistry{}, err
	}

	return registry, nil
}

func cloneAction(action Action) Action {
	action.Command = cloneStrings(action.Command)
	action.Env = cloneStringMap(action.Env)
	return action
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(values))
	maps.Copy(cloned, values)

	return cloned
}

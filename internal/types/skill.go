package types

import "time"

// SkillStatus represents the enabled/disabled state of a skill.
type SkillStatus string

const (
	SkillEnabled  SkillStatus = "enabled"
	SkillDisabled SkillStatus = "disabled"
)

// SkillDocument is the unified import format for skills.
type SkillDocument struct {
	Name        string             `json:"name" yaml:"name"`
	Description string             `json:"description" yaml:"description"`
	Body        string             `json:"body" yaml:"body"`
	Tools       []SkillToolBinding `json:"tools,omitempty" yaml:"tools,omitempty"`
}

// SkillMeta holds the full persisted metadata for a skill.
type SkillMeta struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Body        string             `json:"body"`
	Status      SkillStatus        `json:"status"`
	Tools       []SkillToolBinding `json:"tools"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

// SkillToolBinding declares a tool associated with a skill.
type SkillToolBinding struct {
	Name        string                 `json:"name" yaml:"name"`
	Description string                 `json:"description" yaml:"description"`
	InputSchema map[string]interface{} `json:"input_schema" yaml:"input_schema"`
	Binding     string                 `json:"binding" yaml:"binding"`
}

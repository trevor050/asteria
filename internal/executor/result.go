package executor

import "asteria/internal/session"

type SkillResult struct {
	UpdatedFiles []session.WorkingFile   `json:"updatedFiles"`
	Session      session.SessionSnapshot `json:"session"`
	Message      string                  `json:"message,omitempty"`
}

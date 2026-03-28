package tts

import "fmt"

type ErrMissingAPIKey struct {
	Provider string
	Field    string
}

func (e ErrMissingAPIKey) Error() string {
	return fmt.Sprintf("tts provider %q requires %s to be set", e.Provider, e.Field)
}

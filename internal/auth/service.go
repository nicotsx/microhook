package auth

import "github.com/nicotsx/microhook/internal/config"

type Service struct {
	tokens []config.Token
}

func New(cfg config.AuthConfig) (*Service, error) {
	tokens := make([]config.Token, len(cfg.Tokens))
	copy(tokens, cfg.Tokens)

	return &Service{tokens: tokens}, nil
}

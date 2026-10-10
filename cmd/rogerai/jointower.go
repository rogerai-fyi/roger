package main

import (
	"fmt"

	"rogerai.fm/roger/v6/internal/joininvite"
)

// joinTower joins a standalone Tower with an invite (features/join/invite.feature section 2).
func joinTower(cfg config, in joininvite.Invite, pos, flags []string) error {
	return fmt.Errorf("joining a Tower with an invite is not built yet")
}

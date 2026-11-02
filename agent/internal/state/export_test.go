package state

// Pending returns how many requests wait in the writer's queue (tests use it to build a known batch).
func Pending(s *Store) int { return len(s.q) }

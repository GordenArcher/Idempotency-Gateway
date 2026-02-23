package store

import "github.com/GordenArcher/Idempotency-Gateway/models"

type Store interface {
	Get(key string) *models.CachedEntry
	Set(key string, entry *models.CachedEntry)
	StartSweeper()
}

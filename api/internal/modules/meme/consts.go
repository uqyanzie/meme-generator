package meme

import "time"

const (
	maxPresignTTL  = 15 * time.Minute
	maxUploadBytes = int64(5 << 20) // 5 MiB
)

const (
	defaultLimit = 20
	maxLimit     = 100
)

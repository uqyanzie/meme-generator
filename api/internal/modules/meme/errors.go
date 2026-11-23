package meme

import "errors"

var (
	ErrNotFound          = errors.New("meme tidak ditemukan")
	ErrNotOwned          = errors.New("bukan pemilik meme")
	ErrInvalidFile       = errors.New("file gambar tidak valid")
	ErrFileTooLarge      = errors.New("ukuran file melebihi batas maksimum 5 MB")
	ErrNotSubscribedGate = errors.New("butuh langganan aktif untuk membuat meme")
)

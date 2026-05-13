package models

type User struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Registration string `json:"registration"`
	//Enabled      string `json:"enabled"` // "0" ou "1"
}

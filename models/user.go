package models

type User struct {
	ID           int    `json:"id,omitempty"`
	Name         string `json:"name"`
	Registration string `json:"registration"`
}

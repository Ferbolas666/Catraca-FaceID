package database

import (
	"database/sql"
	"fmt"

	_ "github.com/lib/pq"

	"idface-sync/config"
)

var DB *sql.DB

func Connect() error {

	connStr := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		config.DB_HOST,
		config.DB_PORT,
		config.DB_USER,
		config.DB_PASSWORD,
		config.DB_NAME,
	)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return err
	}

	err = db.Ping()
	if err != nil {
		return err
	}

	DB = db

	fmt.Println("POSTGRES CONECTADO")

	return nil
}

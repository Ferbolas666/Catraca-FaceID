package sync

import (
	"fmt"

	"idface-sync/database"
	"idface-sync/idface"
	"idface-sync/models"
)

func SyncUsers(session string) error {

	rows, err := database.DB.Query(`
		SELECT
			cod_usuario,
			nome,
			COALESCE(matricula, cod_usuario::text),
			foto
		FROM usuarios
		WHERE ativo = 'S'
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {

		var user models.User
		var fotoBytes []byte

		err := rows.Scan(
			&user.ID,
			&user.Name,
			&user.Registration,
			&fotoBytes,
		)
		if err != nil {
			fmt.Println("ERRO SCAN:", err)
			continue
		}

		err = idface.CreateOrModifyUser(session, user)
		if err != nil {
			fmt.Println("ERRO USER:", err)
			continue
		}

		if len(fotoBytes) > 0 {
			err = idface.SetUserImage(session, user.ID, fotoBytes)
			if err != nil {
				fmt.Println("ERRO FOTO:", err)
			}
		}

		fmt.Println("SINCRONIZADO:", user.Name)
	}

	return nil
}

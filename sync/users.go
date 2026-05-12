package sync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"idface-sync/config"
	"idface-sync/database"
	"idface-sync/idface"
	"idface-sync/models"
)

// SyncUsers sincroniza com o dispositivo: envia apenas os usuários autorizados para hoje
// e remove todos os demais que existem no dispositivo.
func SyncUsers(session string) error {
	// 1. Obter matrículas autorizadas para hoje (baseado em itens_vendas)
	rowsPerm, err := database.DB.Query(`
		SELECT usuarios_consumo_restaurante
		FROM itens_vendas
		WHERE data_pedido = CURRENT_DATE
		  AND usuarios_consumo_restaurante IS NOT NULL
		  AND jsonb_array_length(usuarios_consumo_restaurante) > 0
	`)
	if err != nil {
		return fmt.Errorf("erro ao consultar permissões: %v", err)
	}
	defer rowsPerm.Close()

	allowedMatriculas := make(map[string]bool)
	for rowsPerm.Next() {
		var jsonArray string
		if err := rowsPerm.Scan(&jsonArray); err != nil {
			continue
		}
		var matriculas []string
		if err := json.Unmarshal([]byte(jsonArray), &matriculas); err != nil {
			continue
		}
		for _, m := range matriculas {
			parts := strings.Split(m, " - ")
			if len(parts) >= 2 {
				mat := strings.TrimSpace(parts[len(parts)-1])
				allowedMatriculas[mat] = true
			}
		}
	}

	if len(allowedMatriculas) == 0 {
		fmt.Println("Nenhum usuário autorizado hoje. Removendo todos os usuários do dispositivo.")
		// Remove todos os usuários do dispositivo
		return removeAllDeviceUsers(session)
	}

	// 2. Enviar usuários autorizados (cria/atualiza)
	matriculasList := make([]string, 0, len(allowedMatriculas))
	for mat := range allowedMatriculas {
		matriculasList = append(matriculasList, mat)
	}

	query := fmt.Sprintf(`
		SELECT cod_usuario, nome, matricula, foto
		FROM usuarios
		WHERE ativo = 'S' AND matricula IN (%s)
	`, formatPlaceholders(len(matriculasList)))

	args := make([]interface{}, len(matriculasList))
	for i, mat := range matriculasList {
		args[i] = mat
	}

	rows, err := database.DB.Query(query, args...)
	if err != nil {
		return fmt.Errorf("erro ao buscar usuários: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var user models.User
		var fotoBytes []byte
		if err := rows.Scan(&user.ID, &user.Name, &user.Registration, &fotoBytes); err != nil {
			fmt.Println("ERRO SCAN:", err)
			continue
		}
		if err := idface.CreateOrModifyUser(session, user); err != nil {
			fmt.Printf("ERRO AO ENVIAR %s: %v\n", user.Name, err)
			continue
		}
		if len(fotoBytes) > 0 {
			if err := idface.SetUserImage(session, user.ID, fotoBytes); err != nil {
				fmt.Printf("ERRO FOTO %s: %v\n", user.Name, err)
			}
		}
		fmt.Printf("SINCRONIZADO: %s (matrícula %s)\n", user.Name, user.Registration)
	}

	// 3. Remover usuários do dispositivo que não estão na lista autorizada
	if err := removeUnauthorizedUsers(session, allowedMatriculas); err != nil {
		fmt.Printf("Erro ao remover usuários não autorizados: %v\n", err)
	}

	return nil
}

// removeUnauthorizedUsers busca todos os usuários do dispositivo e remove aqueles cuja matrícula não está em allowedMatriculas.
func removeUnauthorizedUsers(session string, allowedMatriculas map[string]bool) error {
	deviceUsers, err := getAllDeviceUsers(session)
	if err != nil {
		return err
	}

	for _, u := range deviceUsers {
		if !allowedMatriculas[u.Registration] {
			fmt.Printf("Removendo usuário não autorizado: %s (ID %d, matrícula %s)\n", u.Name, u.ID, u.Registration)
			if err := deleteUserFromDevice(session, u.ID); err != nil {
				fmt.Printf("Falha ao remover usuário %d: %v\n", u.ID, err)
			}
		}
	}
	return nil
}

// removeAllDeviceUsers remove todos os usuários do dispositivo.
func removeAllDeviceUsers(session string) error {
	deviceUsers, err := getAllDeviceUsers(session)
	if err != nil {
		return err
	}
	for _, u := range deviceUsers {
		fmt.Printf("Removendo usuário (nenhum autorizado hoje): %s (ID %d)\n", u.Name, u.ID)
		if err := deleteUserFromDevice(session, u.ID); err != nil {
			fmt.Printf("Falha ao remover %d: %v\n", u.ID, err)
		}
	}
	return nil
}

// getAllDeviceUsers retorna todos os usuários cadastrados no dispositivo.
func getAllDeviceUsers(session string) ([]struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Registration string `json:"registration"`
	Enabled      string `json:"enabled"`
}, error) {
	url := fmt.Sprintf("http://%s/load_objects.fcgi?session=%s", config.IDFACE_IP, session)
	payload := map[string]interface{}{"object": "users"}
	jsonPayload, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonPayload))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Users []struct {
			ID           int    `json:"id"`
			Name         string `json:"name"`
			Registration string `json:"registration"`
			Enabled      string `json:"enabled"`
		} `json:"users"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Users, nil
}

// deleteUserFromDevice remove um usuário específico do dispositivo via destroy_objects.fcgi.
func deleteUserFromDevice(session string, userID int) error {
	url := fmt.Sprintf("http://%s/destroy_objects.fcgi?session=%s", config.IDFACE_IP, session)
	payload := map[string]interface{}{
		"object": "users",
		"values": []map[string]interface{}{
			{"id": userID},
		},
	}
	jsonPayload, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonPayload))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d ao remover usuário %d", resp.StatusCode, userID)
	}
	return nil
}

// formatPlaceholders gera placeholders $1, $2...
func formatPlaceholders(n int) string {
	if n == 0 {
		return ""
	}
	placeholders := make([]string, n)
	for i := 0; i < n; i++ {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	return strings.Join(placeholders, ", ")
}

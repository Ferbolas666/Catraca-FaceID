package sync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"idface-sync/config"
	"idface-sync/database"
	"idface-sync/idface"
	"idface-sync/models"
)

// SyncUsers sincroniza com o dispositivo:
// - Atualiza a tabela consumo_controle com os usuários dos pedidos de hoje (entrada_ocorrida = FALSE)
// - Envia para o dispositivo apenas os usuários que ainda não entraram e que estão dentro do horário permitido
// - Remove do dispositivo usuários que não estão em nenhum pedido de hoje
func SyncUsers(session string) error {
	// 1. Atualizar a tabela consumo_controle a partir dos pedidos de hoje
	if err := refreshConsumoControle(); err != nil {
		fmt.Printf("Erro ao atualizar consumo_controle: %v\n", err)
	}

	// 2. Obter lista de matrículas autorizadas (todas que aparecem em consumo_controle hoje)
	allowedMatriculas, err := getAllowedMatriculasFromControle()
	if err != nil {
		return fmt.Errorf("erro ao obter autorizados: %v", err)
	}

	if len(allowedMatriculas) == 0 {
		fmt.Println("Nenhum usuário autorizado hoje. Removendo todos os usuários do dispositivo.")
		return removeAllDeviceUsers(session)
	}

	// 3. Buscar usuários que ainda NÃO entraram hoje e que estão dentro do horário atual
	rows, err := database.DB.Query(`
		SELECT u.cod_usuario, u.nome, u.matricula, u.foto,
		       cc.hora_inicio, cc.hora_fim
		FROM usuarios u
		JOIN consumo_controle cc ON cc.user_id = u.cod_usuario
		WHERE cc.data_pedido = CURRENT_DATE
		  AND cc.entrada_ocorrida = FALSE
		  AND CURRENT_TIME BETWEEN cc.hora_inicio AND cc.hora_fim
	`)
	if err != nil {
		return fmt.Errorf("erro ao buscar usuários não autorizados: %v", err)
	}
	defer rows.Close()

	enviados := 0
	for rows.Next() {
		var user models.User
		var fotoBytes []byte
		var horaInicio, horaFim string
		if err := rows.Scan(&user.ID, &user.Name, &user.Registration, &fotoBytes, &horaInicio, &horaFim); err != nil {
			fmt.Println("ERRO SCAN:", err)
			continue
		}
		fmt.Printf("HORÁRIO PERMITIDO para %s: %s - %s (agora: %s)\n", user.Name, horaInicio, horaFim, time.Now().Format("15:04:05"))

		// Envia usuário
		if err := idface.CreateOrModifyUser(session, user); err != nil {
			fmt.Printf("ERRO AO ENVIAR %s: %v\n", user.Name, err)
			continue
		}
		if len(fotoBytes) > 0 {
			if err := idface.SetUserImage(session, user.ID, fotoBytes); err != nil {
				fmt.Printf("ERRO FOTO %s: %v\n", user.Name, err)
			}
		}
		// Associa regra de acesso
		if err := idface.AddUserToAccessRule(session, user.ID, 1); err != nil {
			fmt.Printf("ERRO REGRA DE ACESSO %s: %v\n", user.Name, err)
		} else {
			fmt.Printf("Regra de acesso associada para %s\n", user.Name)
		}
		fmt.Printf("SINCRONIZADO: %s (matrícula %s)\n", user.Name, user.Registration)
		enviados++
	}
	fmt.Printf("Total de usuários enviados nesta rodada: %d\n", enviados)

	// 4. Remover do dispositivo usuários que NÃO estão em nenhum pedido de hoje
	if err := removeUnauthorizedUsers(session, allowedMatriculas); err != nil {
		fmt.Printf("Erro ao remover usuários não autorizados: %v\n", err)
	}

	return nil
}

// refreshConsumoControle insere registros em consumo_controle para cada (item_venda, usuário) dos pedidos de hoje,
// com horários baseados no grupo do produto.
func refreshConsumoControle() error {
	rows, err := database.DB.Query(`
		SELECT cod_itens_venda, cod_produto, usuarios_consumo_restaurante
		FROM itens_vendas
		WHERE data_pedido = CURRENT_DATE
		  AND usuarios_consumo_restaurante IS NOT NULL
		  AND jsonb_array_length(usuarios_consumo_restaurante) > 0
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var itemVendaID int
		var codProduto int
		var jsonArray string
		if err := rows.Scan(&itemVendaID, &codProduto, &jsonArray); err != nil {
			continue
		}

		// Buscar o cod_grupo do produto na tabela produtos
		var codGrupo int
		err = database.DB.QueryRow(`SELECT cod_grupo FROM produtos WHERE cod_produto = $1`, codProduto).Scan(&codGrupo)
		if err != nil {
			fmt.Printf("Erro ao buscar grupo do produto %d: %v\n", codProduto, err)
			continue
		}

		// Definir horários conforme o grupo
		var horaInicio, horaFim string
		switch codGrupo {
		case 2: // Café da manhã
			horaInicio = "06:00:00"
			horaFim = "08:00:00"
		case 3: // Almoço
			horaInicio = "10:30:00"
			horaFim = "14:00:00"
		case 4: // Jantar
			horaInicio = "17:00:00"
			horaFim = "21:00:00"
		default:
			fmt.Printf("Grupo %d não mapeado para o produto %d, ignorando\n", codGrupo, codProduto)
			continue
		}

		var matriculas []string
		if err := json.Unmarshal([]byte(jsonArray), &matriculas); err != nil {
			continue
		}
		for _, m := range matriculas {
			parts := strings.Split(m, " - ")
			if len(parts) < 2 {
				continue
			}
			matricula := strings.TrimSpace(parts[len(parts)-1])
			var userID int
			err = database.DB.QueryRow(`SELECT cod_usuario FROM usuarios WHERE matricula = $1`, matricula).Scan(&userID)
			if err != nil {
				fmt.Printf("Matrícula %s não encontrada: %v\n", matricula, err)
				continue
			}
			// Insere registro com os horários específicos do grupo
			_, err = database.DB.Exec(`
				INSERT INTO consumo_controle (item_venda_id, user_id, data_pedido, entrada_ocorrida, hora_inicio, hora_fim)
				VALUES ($1, $2, CURRENT_DATE, FALSE, $3, $4)
				ON CONFLICT (item_venda_id, user_id) DO NOTHING
			`, itemVendaID, userID, horaInicio, horaFim)
			if err != nil {
				fmt.Printf("Erro ao inserir consumo_controle: %v\n", err)
			}
		}
	}
	return nil
}

// getAllowedMatriculasFromControle retorna todas as matrículas que aparecem em consumo_controle para hoje
func getAllowedMatriculasFromControle() (map[string]bool, error) {
	rows, err := database.DB.Query(`
		SELECT u.matricula
		FROM consumo_controle cc
		JOIN usuarios u ON u.cod_usuario = cc.user_id
		WHERE cc.data_pedido = CURRENT_DATE
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	allowed := make(map[string]bool)
	for rows.Next() {
		var matricula string
		if err := rows.Scan(&matricula); err != nil {
			continue
		}
		allowed[matricula] = true
	}
	return allowed, nil
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

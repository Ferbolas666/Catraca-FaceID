package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"idface-sync/config"
	"idface-sync/database"
	"idface-sync/idface"
)

var eventQueue = make(chan int, 1000)
var session string

func SetSession(s string) { session = s }

func StartWorker() {
	go func() {
		for portalID := range eventQueue {
			idface.Enqueue(func() {
				_ = idface.LiberarCatraca(session, portalID)
				fmt.Println("CATRACA LIBERADA")
			})
		}
	}()
}

// Desabilita um usuário no dispositivo (impede reconhecimento)
func disableUserOnDevice(userID int) error {
	url := fmt.Sprintf("http://%s/update_objects.fcgi?session=%s", config.IDFACE_IP, session)
	payload := map[string]interface{}{
		"object": "users",
		"values": []map[string]interface{}{
			{
				"id":      userID,
				"enabled": "0",
			},
		},
	}
	jsonData, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("falha ao desabilitar usuário: %d", resp.StatusCode)
	}
	fmt.Printf("Usuário %d desabilitado no dispositivo\n", userID)
	return nil
}

// Habilita um usuário no dispositivo
func enableUserOnDevice(userID int) error {
	url := fmt.Sprintf("http://%s/update_objects.fcgi?session=%s", config.IDFACE_IP, session)
	payload := map[string]interface{}{
		"object": "users",
		"values": []map[string]interface{}{
			{
				"id":      userID,
				"enabled": "1",
			},
		},
	}
	jsonData, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("falha ao habilitar usuário: %d", resp.StatusCode)
	}
	fmt.Printf("Usuário %d habilitado no dispositivo\n", userID)
	return nil
}

func Webhook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	fmt.Printf("\n================================\nROTA: %s\nBODY:\n%s\n================================\n", r.URL.Path, string(body))

	switch r.URL.Path {
	case "/device_is_alive.fcgi":
		w.WriteHeader(http.StatusNoContent)
		fmt.Println("RESPOSTA device_is_alive: 204 No Content")
		return
	case "/new_user_identified.fcgi":
		handleUserIdentified(w, body)
	case "/access_log.fcgi":
		handleAccessLog(w, body)
	case "/monitor/operation_mode":
		handleOperationMode(w, body)
	case "/monitor/dao":
		handleMonitorDAO(w, body)
	case "/monitor/catra_event":
		w.WriteHeader(http.StatusNoContent)
		fmt.Println("Monitor/catra_event recebido (evento de catraca)")
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func handleMonitorDAO(w http.ResponseWriter, body []byte) {
	var data struct {
		ObjectChanges []struct {
			Object string `json:"object"`
			Type   string `json:"type"`
			Values struct {
				UserID     string `json:"user_id"`
				PortalID   string `json:"portal_id"`
				Confidence string `json:"confidence"`
				Time       string `json:"time"`
			} `json:"values"`
		} `json:"object_changes"`
	}

	if err := json.Unmarshal(body, &data); err != nil {
		fmt.Printf("Erro ao parsear monitor/dao: %v\n", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	for _, change := range data.ObjectChanges {
		if change.Object == "access_logs" && change.Type == "inserted" {
			userID, _ := strconv.Atoi(change.Values.UserID)
			portalID, _ := strconv.Atoi(change.Values.PortalID)
			confidence, _ := strconv.Atoi(change.Values.Confidence)

			// Determina o tipo de evento
			eventType := "unknown"
			if portalID == 1 {
				eventType = "entry"
			} else if portalID == 2 {
				eventType = "exit"
			}

			// 1. Insere na tabela access_logs
			_, err := database.DB.Exec(`
                INSERT INTO access_log (user_id, portal_id, event_time, confidence, event_type)
                VALUES ($1, $2, NOW(), $3, $4)
            `, userID, portalID, confidence, eventType)
			if err != nil {
				fmt.Printf("Erro ao inserir access_log: %v\n", err)
			}

			// 2. Atualiza o status na tabela user_session
			if portalID == 1 { // entrada
				_, err = database.DB.Exec(`
                    INSERT INTO user_session (user_id, status, last_event_time, last_portal_id)
                    VALUES ($1, 'inside', NOW(), $2)
                    ON CONFLICT (user_id) DO UPDATE SET
                        status = 'inside',
                        last_event_time = NOW(),
                        last_portal_id = $2
                `, userID, portalID)
			} else if portalID == 2 { // saída
				_, err = database.DB.Exec(`
                    INSERT INTO user_session (user_id, status, last_event_time, last_portal_id)
                    VALUES ($1, 'outside', NOW(), $2)
                    ON CONFLICT (user_id) DO UPDATE SET
                        status = 'outside',
                        last_event_time = NOW(),
                        last_portal_id = $2
                `, userID, portalID)
			}
			if err != nil {
				fmt.Printf("Erro ao atualizar user_session: %v\n", err)
			}

			fmt.Printf("Log registrado: user %d, portal %d (%s), confiança %d\n", userID, portalID, eventType, confidence)
		}
	}

	w.WriteHeader(http.StatusNoContent)
	fmt.Println("Monitor/dao processado")
}

func handleUserIdentified(w http.ResponseWriter, body []byte) {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	userMatricula := values.Get("registration")
	userName := values.Get("user_name")
	portalID, _ := strconv.Atoi(values.Get("portal_id"))

	// 1. Buscar o cod_usuario (userID) a partir da matrícula
	var userID int
	err = database.DB.QueryRow(`
        SELECT cod_usuario 
        FROM usuarios 
        WHERE matricula = $1
    `, userMatricula).Scan(&userID)
	if err != nil {
		if err == sql.ErrNoRows {
			fmt.Printf("❌ Matrícula %s não encontrada no sistema.\n", userMatricula)
		} else {
			fmt.Printf("Erro ao buscar usuário: %v\n", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "denied", "reason": "Usuário não cadastrado"})
		return
	}

	// 2. Buscar a lista de matrículas permitidas para hoje (consumo)
	rows, err := database.DB.Query(`
        SELECT usuarios_consumo_restaurante
        FROM itens_vendas
        WHERE data_pedido = CURRENT_DATE
          AND usuarios_consumo_restaurante IS NOT NULL
          AND jsonb_array_length(usuarios_consumo_restaurante) > 0
    `)
	if err != nil {
		fmt.Printf("Erro na consulta de regras: %v\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	allowedMatriculas := make(map[string]bool)
	for rows.Next() {
		var jsonArray string
		if err := rows.Scan(&jsonArray); err != nil {
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

	// 3. Verificar se a matrícula está autorizada para hoje (regra de consumo)
	if !allowedMatriculas[userMatricula] {
		fmt.Printf("❌ Acesso NEGADO (consumo): %s (ID %d, matrícula %s) - não está na lista de hoje\n", userName, userID, userMatricula)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "denied", "reason": "Usuário não autorizado para hoje"})
		return
	}

	// 4. Consultar status atual na tabela user_session
	var status string
	err = database.DB.QueryRow("SELECT status FROM user_session WHERE user_id = $1", userID).Scan(&status)
	if err == sql.ErrNoRows {
		status = "outside" // usuário nunca entrou, considera fora
	} else if err != nil {
		fmt.Printf("Erro ao consultar status: %v\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	const ENTRADA = 1
	const SAIDA = 2

	allowed := false
	newStatus := status

	if portalID == ENTRADA {
		if status == "outside" {
			allowed = true
			newStatus = "inside"
			fmt.Printf("✅ Entrada permitida: %s (ID %d)\n", userName, userID)
		} else {
			fmt.Printf("🚫 Entrada NEGADA (já dentro): %s (ID %d)\n", userName, userID)
		}
	} else if portalID == SAIDA {
		// Saída sempre permitida, mas só atualiza se estava dentro
		allowed = true
		if status == "inside" {
			newStatus = "outside"
			fmt.Printf("🚪 Saída registrada: %s (ID %d)\n", userName, userID)
		} else {
			fmt.Printf("⚠️ Saída ignorada (já estava fora): %s (ID %d)\n", userName, userID)
		}
	} else {
		// Outros portais? Decide-se permitir
		allowed = true
		fmt.Printf("Portal %d não mapeado, permitindo acesso para %s\n", portalID, userName)
	}

	// 5. Se permitido, atualiza o status no banco e responde allowed
	if allowed {
		if newStatus != status {
			_, err = database.DB.Exec(`
                INSERT INTO user_session (user_id, status, last_portal_id, last_event_time)
                VALUES ($1, $2, $3, NOW())
                ON CONFLICT (user_id) DO UPDATE SET
                    status = $2,
                    last_portal_id = $3,
                    last_event_time = NOW()
            `, userID, newStatus, portalID)
			if err != nil {
				fmt.Printf("Erro ao atualizar status: %v\n", err)
			}
		}
		resp := map[string]string{"status": "allowed"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	} else {
		resp := map[string]string{"status": "denied", "reason": "Usuário já está dentro"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}
}

func handleAccessLog(w http.ResponseWriter, body []byte) {
	w.WriteHeader(http.StatusNoContent)
	fmt.Println("Access_log confirmado")
}

func handleOperationMode(w http.ResponseWriter, body []byte) {
	resp := map[string]interface{}{
		"status":      "ok",
		"server_time": time.Now().Unix(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

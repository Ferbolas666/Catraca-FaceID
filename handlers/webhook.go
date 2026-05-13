package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	// Apenas registra o log, sem modificar estado do usuário
	fmt.Println("Monitor/dao recebido (log de acesso)")
	w.WriteHeader(http.StatusNoContent)
}

func handleUserIdentified(w http.ResponseWriter, body []byte) {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	userMatricula := values.Get("registration")
	userName := values.Get("user_name")

	// 1. Buscar o cod_usuario (userID) a partir da matrícula
	var userID int
	err = database.DB.QueryRow(`
        SELECT cod_usuario 
        FROM usuarios 
        WHERE matricula = $1
    `, userMatricula).Scan(&userID)
	if err != nil {
		if err == sql.ErrNoRows {
			fmt.Printf("❌ Matrícula %s não encontrada ou inativa no sistema.\n", userMatricula)
		} else {
			fmt.Printf("Erro ao buscar usuário: %v\n", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "denied", "reason": "Usuário não cadastrado"})
		return
	}

	// 2. Buscar a lista de matrículas permitidas para hoje
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

	// 3. Verifica se a matrícula está na lista permitida
	if allowedMatriculas[userMatricula] {
		fmt.Printf("✅ Acesso permitido: %s (ID %d, matrícula %s)\n", userName, userID, userMatricula)
		resp := map[string]string{"status": "allowed", "result": "success", "action": "open"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	} else {
		fmt.Printf("❌ Acesso NEGADO: %s (ID %d, matrícula %s) - não está na lista de hoje\n", userName, userID, userMatricula)
		resp := map[string]string{"status": "denied", "reason": "Usuário não autorizado para hoje"}
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
